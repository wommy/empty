package endpoint

import (
	"context"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

func TestUserValueDash(t *testing.T) {
	for _, c := range []struct {
		p    zfs.Prop
		want string
	}{
		{zfs.Prop{Value: "-", Source: "-"}, ""},
		{zfs.Prop{Value: "-", Source: "local"}, "-"},
		{zfs.Prop{Value: "-", Source: "inherited from tank"}, ""},
		{zfs.Prop{Value: "x", Source: "local"}, "x"},
	} {
		if got := userValue(c.p); got != c.want {
			t.Errorf("userValue(%+v) = %q, want %q", c.p, got, c.want)
		}
	}
}

// A locally set marker of "-" is a value, not "unset": a dataset whose
// csi_volume_name is literally "-" names another volume and is not served
// under its own id (it read as "no marker", which matches any id).
func TestLiteralDashMarkerIsAValue(t *testing.T) {
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, "tank/v", nil, false); err != nil {
		t.Fatal(err)
	}
	ep, err := New(z, Config{Parent: "tank/v", Now: func() time.Time { return time.Unix(2_000_000_000, 0) }})
	if err != nil {
		t.Fatal(err)
	}
	n := ep.names
	if err := z.CreateFilesystem(ctx, "tank/v/a", map[string]string{
		n.managed(): "true", n.success(): "true", n.volumeName(): "-",
	}, false); err != nil {
		t.Fatal(err)
	}
	tbl, err := ep.scan(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := tbl.Get("a"); r.VolumeName != "-" || r.OwnedVolume() {
		t.Fatalf("row %+v: a local \"-\" read as unset", r)
	}
	if _, err := ep.GetVolume(ctx, "a"); CodeOf(err) != NotFound {
		t.Fatalf("GetVolume: %v, want NotFound", err)
	}
}
