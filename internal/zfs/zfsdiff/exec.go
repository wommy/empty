package zfsdiff

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/wommy/empty/internal/zfs"
)

// Step is one command: the arguments after "zfs", or the pseudo command
// "@sendrecv SNAP DST" (zfs send -p SNAP | zfs recv DST). "$R" stands for
// the sequence's root dataset.
type Step struct{ Args []string }

func (s Step) String() string {
	if len(s.Args) > 0 && s.Args[0] == "@sendrecv" {
		return strings.Join(s.Args, " ")
	}
	return "zfs " + strings.Join(s.Args, " ")
}

// Seq is a command sequence run under a fresh root.
type Seq struct {
	Name  string
	Notes []string // "# " comment lines
	Steps []Step
}

// Parse reads the corpus format: one command per line ("zfs ..." or
// "@sendrecv ..."), "#" comments, blank lines ignored.
func Parse(name, text string) (Seq, error) {
	s := Seq{Name: name}
	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "#"):
			s.Notes = append(s.Notes, strings.TrimSpace(strings.TrimPrefix(line, "#")))
		case strings.HasPrefix(line, "zfs "):
			s.Steps = append(s.Steps, Step{strings.Fields(line)[1:]})
		case strings.HasPrefix(line, "@sendrecv "):
			f := strings.Fields(line)
			if len(f) != 3 {
				return s, fmt.Errorf("%s:%d: @sendrecv SNAP DST", name, i+1)
			}
			s.Steps = append(s.Steps, Step{f})
		default:
			return s, fmt.Errorf("%s:%d: unknown line %q", name, i+1, line)
		}
	}
	return s, nil
}

// Format writes s in the corpus format.
func (s Seq) Format() string {
	var b strings.Builder
	for _, n := range s.Notes {
		fmt.Fprintf(&b, "# %s\n", n)
	}
	for _, st := range s.Steps {
		b.WriteString(st.String() + "\n")
	}
	return b.String()
}

// SendRecver is a backend that can do `zfs send -p SNAP | zfs recv DST`.
type SendRecver interface {
	SendRecv(ctx context.Context, snap, dst string) error
}

// ModelRunner adapts a Model to SendRecver.
type ModelRunner struct{ *Model }

func (m ModelRunner) SendRecv(_ context.Context, snap, dst string) error {
	err := m.Model.SendRecv(snap, dst)
	var w *exitErrWrap
	if errors.As(err, &w) {
		return &zfs.ExitError{Code: w.e.code, Stderr: w.e.msg + "\n"}
	}
	return err
}

// Projection is the property list compared after every step: everything
// the endpoint reads, plus holds and inheritance-relevant props; not the
// space or time properties (used, available, creation), which differ.
var Projection = []string{
	"type", "origin", "volsize", "volblocksize", "quota", "refquota", "reservation",
	"refreservation", "defer_destroy", "userrefs", "mountpoint", "mounted", "canmount",
	"sharenfs", "compression", "recordsize", "atime", "relatime", "sync", "logbias",
	"dedup", "xattr", "acltype", "snapdir", "copies", "zd:a", "zd:b",
}

// Outcome is what one step did.
type Outcome struct {
	Cmd    string
	Code   int
	Class  Class
	Kind   zfs.Kind
	Stderr string
	Proj   []string // sorted "name\tprop\tvalue\tsource", root shown as $R
	// Unmodelled: the model refused the step (exit 3); comparison stops.
	Unmodelled bool
}

// Exec runs seq under root (created with create -p first) and records each
// step's outcome and the projection after it. The setup step is
// outcome 0.
func Exec(ctx context.Context, r zfs.Runner, root string, seq Seq) []Outcome {
	steps := append([]Step{{[]string{"create", "-p", "$R"}}}, seq.Steps...)
	out := make([]Outcome, 0, len(steps))
	for _, st := range steps {
		args := make([]string, len(st.Args))
		for i, a := range st.Args {
			args[i] = strings.ReplaceAll(a, "$R", root)
		}
		o := Outcome{Cmd: st.String()}
		var err error
		if args[0] == "@sendrecv" {
			if sr, ok := r.(SendRecver); ok {
				err = sr.SendRecv(ctx, args[1], args[2])
			} else {
				err = &zfs.ExitError{Code: 3, Stderr: "send/recv unsupported\n"}
			}
		} else {
			_, err = r.Run(ctx, "zfs", args...)
		}
		if err != nil {
			var ee *zfs.ExitError
			if errors.As(err, &ee) {
				o.Code, o.Stderr = ee.Code, ee.Stderr
			} else {
				o.Code, o.Stderr = -1, err.Error()
			}
			o.Class, o.Kind = ClassOf(o.Code, o.Stderr), KindOf(o.Stderr)
			o.Unmodelled = o.Code == 3
		}
		o.Proj = project(ctx, r, root)
		out = append(out, o)
		if o.Unmodelled {
			break
		}
	}
	return out
}

func project(ctx context.Context, r zfs.Runner, root string) []string {
	b, err := r.Run(ctx, "zfs", "get", "-Hp", "-o", "name,property,value,source", "-r",
		"-t", "filesystem,volume,snapshot", strings.Join(Projection, ","), root)
	if err != nil {
		var ee *zfs.ExitError
		msg := err.Error()
		if errors.As(err, &ee) {
			msg = strings.TrimSpace(ee.Stderr)
		}
		return []string{"!get failed: " + strings.ReplaceAll(msg, root, "$R")}
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l == "" {
			continue
		}
		lines = append(lines, strings.ReplaceAll(l, root, "$R"))
	}
	sort.Strings(lines)
	return lines
}

// Divergence is the first step where two traces disagree.
type Divergence struct {
	Step  int // index into the trace (0 = setup)
	Cmd   string
	Field string // code, class, kind, proj
	A, B  string // the differing values (for proj: the differing lines)
	Sig   string // field + command verb (+ property), for minimisation
}

func (d *Divergence) String() string {
	return fmt.Sprintf("step %d `%s`: %s differs\n  A: %s\n  B: %s", d.Step, d.Cmd, d.Field,
		strings.ReplaceAll(d.A, "\n", "\n     "), strings.ReplaceAll(d.B, "\n", "\n     "))
}

func verb(cmd string) string {
	f := strings.Fields(cmd)
	if f[0] == "zfs" && len(f) > 1 {
		return f[1]
	}
	return f[0]
}

func codeClass(c int) string {
	switch c {
	case 0, 1, 2:
		return fmt.Sprint(c)
	}
	return "other"
}

// Compare returns the first divergence between traces a and b, or nil.
// Comparison ends at an unmodelled step in either.
func Compare(a, b []Outcome) *Divergence {
	for i := 0; i < len(a) && i < len(b); i++ {
		x, y := a[i], b[i]
		if x.Unmodelled || y.Unmodelled {
			return nil
		}
		d := &Divergence{Step: i, Cmd: x.Cmd}
		v := verb(x.Cmd)
		switch {
		case codeClass(x.Code) != codeClass(y.Code):
			d.Field, d.A, d.B = "code", fmt.Sprintf("%d %s", x.Code, strings.TrimSpace(x.Stderr)), fmt.Sprintf("%d %s", y.Code, strings.TrimSpace(y.Stderr))
		case x.Class != y.Class:
			d.Field, d.A, d.B = "class", fmt.Sprintf("%s: %s", x.Class, strings.TrimSpace(x.Stderr)), fmt.Sprintf("%s: %s", y.Class, strings.TrimSpace(y.Stderr))
		case x.Kind != y.Kind:
			d.Field, d.A, d.B = "kind", fmt.Sprintf("%s: %s", x.Kind, strings.TrimSpace(x.Stderr)), fmt.Sprintf("%s: %s", y.Kind, strings.TrimSpace(y.Stderr))
		case !slices.Equal(x.Proj, y.Proj):
			onlyA, onlyB := lineDiff(x.Proj, y.Proj)
			d.Field, d.A, d.B = "proj", strings.Join(onlyA, "\n"), strings.Join(onlyB, "\n")
			d.Sig = "proj:" + v + ":" + projProp(onlyA, onlyB)
		default:
			continue
		}
		if d.Sig == "" {
			d.Sig = d.Field + ":" + v
		}
		return d
	}
	if len(a) != len(b) {
		return &Divergence{Step: min(len(a), len(b)), Field: "length", Sig: "length",
			A: fmt.Sprint(len(a)), B: fmt.Sprint(len(b))}
	}
	return nil
}

func lineDiff(a, b []string) (onlyA, onlyB []string) {
	inB := map[string]bool{}
	for _, l := range b {
		inB[l] = true
	}
	inA := map[string]bool{}
	for _, l := range a {
		inA[l] = true
		if !inB[l] {
			onlyA = append(onlyA, l)
		}
	}
	for _, l := range b {
		if !inA[l] {
			onlyB = append(onlyB, l)
		}
	}
	return
}

func projProp(a, b []string) string {
	for _, l := range append(a, b...) {
		if f := strings.Split(l, "\t"); len(f) >= 2 {
			return f[1]
		}
	}
	return "?"
}

// Minimise drops steps from seq while check still reports a divergence
// with the same signature.
func Minimise(seq Seq, check func(Seq) *Divergence) Seq {
	d := check(seq)
	if d == nil {
		return seq
	}
	sig := d.Sig
	// cut everything after the diverging step (step 0 is setup)
	if d.Step >= 1 && d.Step < len(seq.Steps) {
		t := seq
		t.Steps = slices.Clone(seq.Steps[:d.Step])
		if x := check(t); x != nil && x.Sig == sig {
			seq = t
		}
	}
	for changed := true; changed; {
		changed = false
		for i := len(seq.Steps) - 1; i >= 0; i-- {
			t := seq
			t.Steps = slices.Delete(slices.Clone(seq.Steps), i, i+1)
			if x := check(t); x != nil && x.Sig == sig {
				seq, changed = t, true
			}
		}
	}
	return seq
}
