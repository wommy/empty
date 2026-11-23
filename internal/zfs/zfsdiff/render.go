package zfsdiff

import (
	"fmt"
	"strings"
)

// Render prints a trace compactly: each step's exit status and stderr,
// then the projection lines that changed (+ new, - gone).
func Render(tr []Outcome) string {
	var b strings.Builder
	var prev []string
	for i, o := range tr {
		status := "ok"
		if o.Code != 0 {
			status = fmt.Sprintf("exit %d [%s/%s] %s", o.Code, o.Class, o.Kind, strings.ReplaceAll(strings.TrimSpace(o.Stderr), "\n", " | "))
		}
		fmt.Fprintf(&b, "%2d %s\n   -> %s\n", i, o.Cmd, status)
		if i > 0 {
			gone, added := lineDiff(prev, o.Proj)
			for _, l := range gone {
				if !blank(l) {
					fmt.Fprintf(&b, "   - %s\n", l)
				}
			}
			for _, l := range added {
				if !blank(l) {
					fmt.Fprintf(&b, "   + %s\n", l)
				}
			}
		}
		prev = o.Proj
	}
	return b.String()
}

// blank: a "-" value with source "-" (a property that does not apply).
func blank(l string) bool { return strings.HasSuffix(l, "\t-\t-") }
