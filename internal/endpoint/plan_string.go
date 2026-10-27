package endpoint

import (
	"sort"
	"strconv"
	"strings"
)

// String renders a plan one op per line with properties sorted, so the
// same plan always prints the same text: golden files and logs compare it.
//
//	set tank/v/a volsize=2147483648 refreservation=auto
func (p Plan) String() string {
	var b strings.Builder
	for _, op := range p {
		b.WriteString(op.Kind.String())
		b.WriteByte(' ')
		b.WriteString(op.Target)
		if op.Source != "" {
			b.WriteString(" from=" + op.Source)
		}
		if op.Path != "" {
			b.WriteString(" path=" + op.Path)
		}
		if op.Size != 0 {
			b.WriteString(" size=" + u64(op.Size))
		}
		for _, f := range []struct {
			on   bool
			name string
		}{{op.Sparse, "sparse"}, {op.Recursive, "recursive"}, {op.Defer, "defer"}, {op.OkExists, "ok-exists"}, {op.OkMissing, "ok-missing"}} {
			if f.on {
				b.WriteString(" +" + f.name)
			}
		}
		keys := make([]string, 0, len(op.Props))
		for k := range op.Props {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			q := strconv.Quote(op.Props[k])
			b.WriteString(" " + k + "=" + q[1:len(q)-1])
		}
		b.WriteByte('\n')
	}
	return b.String()
}
