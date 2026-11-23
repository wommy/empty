package endpoint

import (
	"strings"
	"testing"
)

// H-08: CSI snapshot names are opaque (any Unicode up to 128 bytes), so
// "." and ".." must not become the zfs snapshot component: zfs refuses
// both (entity_namecheck), and CreateSnapshot would fail for a legal CSI
// name. snapComponent hashes them like any other name zfs would refuse.
func TestSnapshotComponentNeverDots(t *testing.T) {
	t.Parallel()
	for _, name := range []string{".", ".."} {
		if got := snapComponent("v", name); got == "." || got == ".." || !strings.HasPrefix(got, "csi-") {
			t.Errorf("snapComponent(%q) = %q, want a csi- hash", name, got)
		}
		plan, _, err := pl.PlanCreateSnapshot(NewTable([]Row{vol("v", nil)}), name, "v")
		if err != nil {
			t.Fatal(err)
		}
		if tgt := plan[0].Target; strings.HasSuffix(tgt, "@"+name) {
			t.Errorf("plan would run: zfs snapshot %s", tgt)
		}
	}
	// a name with dots in it is still used as is
	if got := snapComponent("v", "a..b"); got != "a..b" {
		t.Errorf("snapComponent(a..b) = %q", got)
	}
}
