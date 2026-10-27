// Package zfs is a typed client for the zfs(8) command line.
//
// It is deliberately strict where the Go ZFS libraries surveyed are loose:
// the scan (ListColumns) is decoded from `zfs get -j -p` by a strict JSON
// decoder, single-dataset reads from `-Hp` (tab separated, exact numbers),
// every failure is classified into a Kind, and only KindNotFound ever means
// "absent".
package zfs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
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

// Prop is one property value as reported by `zfs get -Hp`.
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
	propRE    = regexp.MustCompile(`^[a-z0-9_.:\-]+$`)
)

// ValidDataset reports whether name is a filesystem/volume name we accept.
func ValidDataset(name string) bool { return len(name) < 256 && datasetRE.MatchString(name) }

// ValidSnapshot reports whether name is a snapshot name we accept.
func ValidSnapshot(name string) bool { return len(name) < 256 && snapRE.MatchString(name) }

// checkProps refuses a property name that is not a plain zfs property
// name, and any value that is not printable ASCII (0x20-0x7e). Every value
// the endpoint writes then reads back byte for byte from the JSON scan: the
// printer drops code points above U+FFFF, and in the C locale it stops at
// the first byte >= 0x80, which fails the whole scan of every dataset
// under the parent (exit 0, truncated JSON). Control characters, newline
// and tab among them, would make `-Hp` text ambiguous (Get).
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
	if err == nil {
		return out, nil
	}
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
	return nil, e
}

// Get returns the requested properties of one dataset or snapshot.
func (c *Client) Get(ctx context.Context, name string, props ...string) (Dataset, error) {
	if !ValidDataset(name) && !ValidSnapshot(name) {
		return Dataset{}, &Error{Kind: KindInvalidProperty, Op: "get", Dataset: name, Stderr: "invalid name"}
	}
	if len(props) == 0 {
		props = []string{"all"}
	}
	out, err := c.run(ctx, "get", name, "get", "-Hp", "-o", "name,property,value,source", strings.Join(props, ","), name)
	if err != nil {
		return Dataset{}, err
	}
	ds, err := parseGet(out)
	if err != nil {
		return Dataset{}, &Error{Op: "get", Dataset: name, Err: err}
	}
	d, ok := ds[name]
	if !ok {
		// zfs printed nothing for the dataset; this is not "absent"
		return Dataset{}, &Error{Op: "get", Dataset: name, Err: errors.New("no output for dataset")}
	}
	return d, nil
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

// parseGet parses `zfs get -Hp -o name,property,value,source` output.
//
// zfs prints user property values raw, so a value with a newline can print
// as lines for any (dataset, property), including a dataset zfs never
// listed: no real line has to be there to collide with, and nothing here
// can tell a forged line from a real one. Get reads only the queried name
// and the health check one native property, so a forged line can at worst
// overwrite a value of that same dataset; List serves only the daemon's
// startup prefix check. The scan that ownership and planning rest on is ListColumns, which
// reads JSON and is immune.
func parseGet(out []byte) (map[string]Dataset, error) {
	res := map[string]Dataset{}
	for i, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		// name and property never contain tabs; a user property value can,
		// so anchor on the first two fields and the last (source)
		f := strings.SplitN(line, "\t", 3)
		last := strings.LastIndexByte(line, '\t')
		if len(f) != 3 || last <= len(f[0])+len(f[1])+1 {
			return nil, fmt.Errorf("line %d: want 4 fields: %q", i+1, line)
		}
		f = []string{f[0], f[1], line[len(f[0])+len(f[1])+2 : last], line[last+1:]}
		d, ok := res[f[0]]
		if !ok {
			d = Dataset{Name: f[0], Props: map[string]Prop{}}
		}
		d.Props[f[1]] = Prop{Value: f[2], Source: f[3]}
		res[f[0]] = d
	}
	return res, nil
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

// List returns datasets under parent (excluding parent itself) with props.
func (c *Client) List(ctx context.Context, parent string, o ListOptions) ([]Dataset, error) {
	if !ValidDataset(parent) {
		return nil, &Error{Kind: KindInvalidProperty, Op: "list", Dataset: parent, Stderr: "invalid name"}
	}
	types := o.Types
	if len(types) == 0 {
		types = []Type{TypeFilesystem, TypeVolume}
	}
	ts := make([]string, len(types))
	for i, t := range types {
		ts[i] = string(t)
	}
	props := o.Props
	if len(props) == 0 {
		props = []string{"type"}
	}
	args := []string{"get", "-Hp", "-o", "name,property,value,source", "-t", strings.Join(ts, ",")}
	switch {
	case o.Depth < 0:
		args = append(args, "-r")
	default:
		args = append(args, "-d", strconv.Itoa(o.Depth+1))
	}
	args = append(args, strings.Join(props, ","), parent)
	out, err := c.run(ctx, "list", parent, args...)
	if err != nil {
		return nil, err
	}
	m, err := parseGet(out)
	if err != nil {
		return nil, &Error{Op: "list", Dataset: parent, Err: err}
	}
	delete(m, parent)
	res := make([]Dataset, 0, len(m))
	for _, d := range m {
		res = append(res, d)
	}
	sort.Slice(res, func(i, j int) bool { return res[i].Name < res[j].Name })
	return res, nil
}

// Columns is a scan result in flat, columnar form: one Prop per (dataset,
// property) in a single slice, instead of a map per dataset. Cell(i, j) is
// dataset i's value for Props[j]; properties zfs did not print read as "-".
type Columns struct {
	Props []string
	Names []string
	Cells []Prop
}

// Cell returns dataset i's value for property j.
func (c *Columns) Cell(i, j int) Prop { return c.Cells[i*len(c.Props)+j] }

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
	// -o and -H do not apply to -j output; -p keeps numbers exact and
	// strings ("--json-int" would turn them into JSON numbers)
	args := []string{"get", "-j", "-p", "-t", strings.Join(ts, ",")}
	if o.Depth < 0 {
		args = append(args, "-r")
	} else {
		args = append(args, "-d", strconv.Itoa(o.Depth+1))
	}
	args = append(args, strings.Join(o.Props, ","), root)
	out, err := c.run(ctx, "list", root, args...)
	if err != nil {
		return nil, err
	}
	// every dataset object has exactly one "properties" member, and the
	// sequence cannot occur inside a string, where '"' is escaped
	rows := bytes.Count(out, []byte(`"properties":{`))
	// a *bytes.Buffer is parsed in place: jsontext copies any other reader
	cols, err := decodeGetJSON(bytes.NewBuffer(out), o.Props, rows)
	if err != nil {
		return nil, &Error{Op: "list", Dataset: root, Err: err}
	}
	return cols, nil
}
