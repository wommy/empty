// Package zfs is a typed client for the zfs(8) command line.
//
// It is deliberately strict where the Go ZFS libraries surveyed are loose:
// every read (Get, List, ListColumns) is decoded from `zfs get -j -p` by a
// strict JSON decoder streaming from zfs's stdout, every failure is
// classified into a Kind, and only KindNotFound ever means "absent".
package zfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Type of a ZFS dataset.
type Type string

const (
	TypeFilesystem Type = "filesystem"
	TypeVolume     Type = "volume"
	TypeSnapshot   Type = "snapshot"
	TypeBookmark   Type = "bookmark"
)

// Prop is one property value as reported by `zfs get -j -p`, with the
// source spelled as `zfs get -Hp` prints it.
type Prop struct {
	Value  string
	Source string // local, default, inherited from <ds>, received, -, ...
}

// Uint parses a numeric property. "-" and "none" read as 0.
func (p Prop) Uint() (uint64, error) {
	if p.Value == "-" || p.Value == "none" || p.Value == "" {
		return 0, nil
	}
	return strconv.ParseUint(p.Value, 10, 64)
}

// Local reports whether the value was set on the dataset itself, here.
// "received" is not local: `zfs recv` of a replication stream carries the
// sender's properties, set by whoever owned the source (ADR 0003). A value
// set locally after the receive overrides it and reads as "local".
func (p Prop) Local() bool { return p.Source == "local" }

// Dataset is a dataset and the properties fetched with it.
type Dataset struct {
	Name  string
	Props map[string]Prop
}

// Type returns the dataset type if it was fetched.
func (d Dataset) Type() Type { return Type(d.Props["type"].Value) }

// Client runs zfs commands through a Runner.
type Client struct {
	Runner Runner
	// Binary defaults to "zfs".
	Binary string
}

// New returns a Client for runner.
func New(r Runner) *Client { return &Client{Runner: r, Binary: "zfs"} }

var (
	// zfs component names: alnum plus - _ . : and space are legal; we refuse
	// space and leading '-' so nothing can be read as a flag.
	datasetRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:\-]*(/[A-Za-z0-9_.:\-]+)*$`)
	snapRE    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:\-]*(/[A-Za-z0-9_.:\-]+)*@[A-Za-z0-9_.:\-]+$`)
	// a property name never starts with '-': Set passes k=v without -o,
	// and Get, List and Inherit pass names as their own argv words
	propRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:\-]*$`)
)

// ValidDataset reports whether name is a filesystem/volume name we accept.
func ValidDataset(name string) bool {
	return len(name) < 256 && datasetRE.MatchString(name) && !dotComponent(name)
}

// ValidSnapshot reports whether name is a snapshot name we accept.
func ValidSnapshot(name string) bool {
	return len(name) < 256 && snapRE.MatchString(name) && !dotComponent(name)
}

// dotComponent reports a "." or ".." component (or snapshot name), which
// zfs refuses (entity_namecheck: NAME_ERR_SELF_REF, NAME_ERR_PARENT_REF).
func dotComponent(name string) bool {
	for _, c := range strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '@' }) {
		if c == "." || c == ".." {
			return true
		}
	}
	return false
}

// checkPropNames refuses a property name that could be read as an option
// or split into two ("a,b"): Get, List and ListColumns join the names into
// one argv word. "all" is refused too: the JSON decoder requires exactly
// the properties asked for.
func checkPropNames(op string, props []string) error {
	if len(props) == 0 {
		return &Error{Kind: KindInvalidProperty, Op: op, Stderr: "no properties requested"}
	}
	for _, p := range props {
		if p == "all" || !propRE.MatchString(p) {
			return &Error{Kind: KindInvalidProperty, Op: op, Stderr: "invalid property name " + strconv.Quote(p)}
		}
	}
	return nil
}

// checkProps refuses a property name that is not a plain zfs property
// name, and any value that is not printable ASCII (0x20-0x7e). Every value
// the endpoint writes then reads back byte for byte from the JSON scan: the
// printer drops code points above U+FFFF, and in the C locale it stops at
// the first byte >= 0x80, which fails the whole scan of every dataset
// under the parent (exit 0, truncated JSON). Control characters, newline
// and tab among them, would make `-Hp` text ambiguous to anything else
// that reads it.
func checkProps(props map[string]string) error {
	for k, v := range props {
		if !propRE.MatchString(k) {
			return &Error{Kind: KindInvalidProperty, Op: "validate", Stderr: "invalid property name " + strconv.Quote(k)}
		}
		for i := 0; i < len(v); i++ {
			if v[i] < 0x20 || v[i] > 0x7e {
				return &Error{Kind: KindInvalidProperty, Op: "validate",
					Stderr: fmt.Sprintf("invalid value for %s: byte %#02x at %d is not printable ASCII", k, v[i], i)}
			}
		}
	}
	return nil
}

func propArgs(props map[string]string) []string {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic argv for tests and logs
	var args []string
	for _, k := range keys {
		args = append(args, "-o", k+"="+props[k])
	}
	return args
}

func (c *Client) bin() string {
	if c.Binary == "" {
		return "zfs"
	}
	return c.Binary
}

// run executes zfs and converts failures into *Error.
func (c *Client) run(ctx context.Context, op, dataset string, args ...string) ([]byte, error) {
	out, err := c.Runner.Run(ctx, c.bin(), args...)
	if err != nil {
		return nil, c.fail(ctx, op, dataset, err)
	}
	return out, nil
}

// getJSON runs `zfs get -j -p ...` and decodes its stdout as it arrives
// (see Streamer) with the strict decoder, into Columns of props. A
// non-zero exit is classified like any other; a document the decoder
// refuses is an unclassified failure.
func (c *Client) getJSON(ctx context.Context, op, dataset string, props []string, args ...string) (*Columns, error) {
	var cols *Columns
	err := stream(ctx, c.Runner, func(r io.Reader) error {
		rows := 0
		if b, ok := r.(*bytes.Buffer); ok {
			// every dataset object has exactly one "properties" member,
			// and the sequence cannot occur inside a string, where '"'
			// is escaped
			rows = bytes.Count(b.Bytes(), []byte(`"properties":{`))
		}
		var err error
		cols, err = decodeGetJSON(r, props, rows)
		return err
	}, c.bin(), args...)
	if err != nil {
		return nil, c.fail(ctx, op, dataset, err)
	}
	return cols, nil
}

// fail converts a command's failure into *Error.
func (c *Client) fail(ctx context.Context, op, dataset string, err error) error {
	e := &Error{Op: op, Dataset: dataset, Err: err}
	var ee *ExitError
	if errors.As(err, &ee) {
		e.Stderr = ee.Stderr
		e.Kind = classify(ee.Stderr)
	}
	if cerr := ctx.Err(); cerr != nil {
		// killed (or never started) because the caller gave up: whatever
		// stderr says, the outcome is unknown, and the context's error says
		// why (deadline or cancel)
		e.Kind, e.Err = KindInterrupted, fmt.Errorf("%w (%w)", err, cerr)
	}
	return e
}

// Get returns the requested properties of one dataset or snapshot, read
// from `zfs get -j -p`. At least one property must be named, each by its
// canonical name (no aliases, no "all"): the decoder requires exactly the
// requested set, and exactly the one dataset asked for.
func (c *Client) Get(ctx context.Context, name string, props ...string) (Dataset, error) {
	if !ValidDataset(name) && !ValidSnapshot(name) {
		return Dataset{}, &Error{Kind: KindInvalidProperty, Op: "get", Dataset: name, Stderr: "invalid name"}
	}
	if err := checkPropNames("get", props); err != nil {
		return Dataset{}, err
	}
	cols, err := c.getJSON(ctx, "get", name, props, "get", "-j", "-p", strings.Join(props, ","), name)
	if err != nil {
		return Dataset{}, err
	}
	if len(cols.Names) != 1 || cols.Names[0] != name {
		// zfs printed nothing (or something else) for the dataset; this
		// is not "absent"
		return Dataset{}, &Error{Op: "get", Dataset: name, Err: fmt.Errorf("zfs printed %q, want only %q", cols.Names, name)}
	}
	return cols.Dataset(0), nil
}

// Exists reports whether name exists. Only a classified not-found is false;
// every other failure is returned.
func (c *Client) Exists(ctx context.Context, name string) (bool, error) {
	_, err := c.Get(ctx, name, "type")
	if err == nil {
		return true, nil
	}
	if IsNotFound(err) {
		return false, nil
	}
	return false, err
}

// CreateFilesystem runs `zfs create [-p] -o k=v... name`.
func (c *Client) CreateFilesystem(ctx context.Context, name string, props map[string]string, parents bool) error {
	if !ValidDataset(name) {
		return &Error{Kind: KindInvalidProperty, Op: "create", Dataset: name, Stderr: "invalid name"}
	}
	if err := checkProps(props); err != nil {
		return err
	}
	args := []string{"create"}
	if parents {
		args = append(args, "-p")
	}
	args = append(args, propArgs(props)...)
	_, err := c.run(ctx, "create", name, append(args, name)...)
	return err
}

// CreateVolume runs `zfs create [-s] -V size -o k=v... name`.
func (c *Client) CreateVolume(ctx context.Context, name string, size uint64, sparse bool, props map[string]string) error {
	if !ValidDataset(name) {
		return &Error{Kind: KindInvalidProperty, Op: "create", Dataset: name, Stderr: "invalid name"}
	}
	if size == 0 {
		return &Error{Kind: KindInvalidProperty, Op: "create", Dataset: name, Stderr: "volsize must be > 0"}
	}
	if err := checkProps(props); err != nil {
		return err
	}
	args := []string{"create"}
	if sparse {
		args = append(args, "-s")
	}
	args = append(args, "-V", strconv.FormatUint(size, 10))
	args = append(args, propArgs(props)...)
	_, err := c.run(ctx, "create", name, append(args, name)...)
	return err
}

// Snapshot runs `zfs snapshot -o k=v... ds@snap`.
func (c *Client) Snapshot(ctx context.Context, name string, props map[string]string) error {
	if !ValidSnapshot(name) {
		return &Error{Kind: KindInvalidProperty, Op: "snapshot", Dataset: name, Stderr: "invalid snapshot name"}
	}
	if err := checkProps(props); err != nil {
		return err
	}
	args := append([]string{"snapshot"}, propArgs(props)...)
	_, err := c.run(ctx, "snapshot", name, append(args, name)...)
	return err
}

// Clone runs `zfs clone -o k=v... snap name`.
func (c *Client) Clone(ctx context.Context, snapshot, name string, props map[string]string) error {
	if !ValidSnapshot(snapshot) || !ValidDataset(name) {
		return &Error{Kind: KindInvalidProperty, Op: "clone", Dataset: name, Stderr: "invalid name"}
	}
	if err := checkProps(props); err != nil {
		return err
	}
	args := append([]string{"clone"}, propArgs(props)...)
	_, err := c.run(ctx, "clone", name, append(args, snapshot, name)...)
	return err
}

// DestroyOptions for Destroy.
type DestroyOptions struct {
	// Recursive destroys children and snapshots (-r). Never -R: dependent
	// clones elsewhere must surface as KindHasClones, not be destroyed.
	Recursive bool
	// Defer marks a snapshot for deferred destruction (-d): it goes away when
	// its last clone does (lzc_destroy_snaps defer=B_TRUE).
	Defer bool
}

// Destroy runs `zfs destroy [-r] [-d] name`.
func (c *Client) Destroy(ctx context.Context, name string, o DestroyOptions) error {
	if !ValidDataset(name) && !ValidSnapshot(name) {
		return &Error{Kind: KindInvalidProperty, Op: "destroy", Dataset: name, Stderr: "invalid name"}
	}
	args := []string{"destroy"}
	if o.Recursive {
		args = append(args, "-r")
	}
	if o.Defer {
		args = append(args, "-d")
	}
	_, err := c.run(ctx, "destroy", name, append(args, name)...)
	return err
}

// Set runs `zfs set k=v... name`.
func (c *Client) Set(ctx context.Context, name string, props map[string]string) error {
	return c.set(ctx, name, props, false)
}

// SetNoMount runs `zfs set -u k=v... name`: the property changes without
// the changelist postfix, so zfs neither mounts nor (re)shares the dataset
// (zfs-set(8); libzfs_dataset.c zfs_prop_set_list_flags). A plain set of
// sharenfs on an unmounted canmount=on filesystem mounts and shares it.
func (c *Client) SetNoMount(ctx context.Context, name string, props map[string]string) error {
	return c.set(ctx, name, props, true)
}

func (c *Client) set(ctx context.Context, name string, props map[string]string, noMount bool) error {
	if len(props) == 0 {
		return nil
	}
	if !ValidDataset(name) && !ValidSnapshot(name) {
		return &Error{Kind: KindInvalidProperty, Op: "set", Dataset: name, Stderr: "invalid name"}
	}
	if err := checkProps(props); err != nil {
		return err
	}
	args := []string{"set"}
	if noMount {
		args = append(args, "-u")
	}
	for _, kv := range propArgs(props) {
		if kv != "-o" {
			args = append(args, kv)
		}
	}
	_, err := c.run(ctx, "set", name, append(args, name)...)
	return err
}

// Inherit runs `zfs inherit prop name`.
func (c *Client) Inherit(ctx context.Context, name, prop string) error {
	if !propRE.MatchString(prop) {
		return &Error{Kind: KindInvalidProperty, Op: "inherit", Dataset: name, Stderr: "invalid property"}
	}
	_, err := c.run(ctx, "inherit", name, "inherit", prop, name)
	return err
}

// Mount runs `zfs mount name`: it mounts a filesystem at its mountpoint
// (canmount=noauto included) and does not share it. A filesystem that is
// already mounted fails with KindExists; a legacy or none mountpoint and
// canmount=off fail unclassified.
func (c *Client) Mount(ctx context.Context, name string) error {
	if !ValidDataset(name) {
		return &Error{Kind: KindInvalidProperty, Op: "mount", Dataset: name, Stderr: "invalid name"}
	}
	_, err := c.run(ctx, "mount", name, "mount", name)
	return err
}

// Promote runs `zfs promote name`.
func (c *Client) Promote(ctx context.Context, name string) error {
	if !ValidDataset(name) {
		return &Error{Kind: KindInvalidProperty, Op: "promote", Dataset: name, Stderr: "invalid name"}
	}
	_, err := c.run(ctx, "promote", name, "promote", name)
	return err
}

// ListOptions for List.
type ListOptions struct {
	Types []Type // default filesystem,volume
	Depth int    // 0 = only direct children when Recursive, -1 = unlimited
	Props []string
}

// List returns datasets under parent (excluding parent itself) with props,
// sorted by name. It reads `zfs get -j -p` like ListColumns, so a property
// value cannot forge a dataset or another property's value.
func (c *Client) List(ctx context.Context, parent string, o ListOptions) ([]Dataset, error) {
	if len(o.Props) == 0 {
		o.Props = []string{"type"}
	}
	cols, err := c.ListColumns(ctx, parent, o)
	if err != nil {
		return nil, err
	}
	res := make([]Dataset, 0, len(cols.Names))
	for i, n := range cols.Names {
		if n != parent {
			res = append(res, cols.Dataset(i))
		}
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res, nil
}

// Columns is a scan result in flat, columnar form: one Prop per (dataset,
// property) in a single slice, instead of a map per dataset. Cell(i, j) is
// dataset i's value for Props[j]; the decoder requires every one printed.
type Columns struct {
	Props []string
	Names []string
	Cells []Prop
}

// Cell returns dataset i's value for property j.
func (c *Columns) Cell(i, j int) Prop { return c.Cells[i*len(c.Props)+j] }

// Dataset returns dataset i with its properties as a map.
func (c *Columns) Dataset(i int) Dataset {
	d := Dataset{Name: c.Names[i], Props: make(map[string]Prop, len(c.Props))}
	for j, p := range c.Props {
		d.Props[p] = c.Cell(i, j)
	}
	return d
}

// Col returns the index of prop in Props, or -1.
func (c *Columns) Col(prop string) int {
	for j, p := range c.Props {
		if p == prop {
			return j
		}
	}
	return -1
}

// ListColumns is List in columnar form, including root itself, read from
// `zfs get -j -p` (OpenZFS 2.4, ADR 0004) by the strict decoder in json.go.
// The JSON printer escapes every value, so a property value cannot forge a
// row, a column or a dataset the way a newline could in `-Hp` text. The
// whole document is decoded before anything is returned: callers plan
// destructive operations from it, so a partial result must never escape.
// Every requested property must be a canonical name (no aliases, no
// "all"): zfs keys its JSON by canonical name, and the decoder requires
// exactly the requested set for every dataset.
func (c *Client) ListColumns(ctx context.Context, root string, o ListOptions) (*Columns, error) {
	if !ValidDataset(root) {
		return nil, &Error{Kind: KindInvalidProperty, Op: "list", Dataset: root, Stderr: "invalid name"}
	}
	types := o.Types
	if len(types) == 0 {
		types = []Type{TypeFilesystem, TypeVolume}
	}
	ts := make([]string, len(types))
	for i, t := range types {
		ts[i] = string(t)
	}
	if err := checkPropNames("list", o.Props); err != nil {
		return nil, err
	}
	// -o and -H do not apply to -j output; -p keeps numbers exact and
	// strings ("--json-int" would turn them into JSON numbers)
	args := []string{"get", "-j", "-p", "-t", strings.Join(ts, ",")}
	if o.Depth < 0 {
		args = append(args, "-r")
	} else {
		args = append(args, "-d", strconv.Itoa(o.Depth+1))
	}
	args = append(args, strings.Join(o.Props, ","), root)
	return c.getJSON(ctx, "list", root, o.Props, args...)
}
