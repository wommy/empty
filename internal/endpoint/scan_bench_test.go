package endpoint

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// recorder keeps the stdout of the last command it ran.
type recorder struct {
	r    zfs.Runner
	last []byte
}

func (c *recorder) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := c.r.Run(ctx, name, args...)
	c.last = out
	return out, err
}

// replay answers every command with the same stdout.
type replay []byte

func (r replay) Run(context.Context, string, ...string) ([]byte, error) { return r, nil }

// BenchmarkScan is the Go-side cost of one whole-parent scan of n rows
// (half owned, committed filesystems, half a CSI snapshot of each): parse
// the `zfs get -Hp` output the simulator printed for it, decode the rows
// and build the Table. The zfs exec itself is not measured.
func BenchmarkScan(b *testing.B) {
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			ctx := context.Background()
			sim := zfstest.New("tank")
			rec := &recorder{r: sim}
			z := zfs.New(rec)
			if err := z.CreateFilesystem(ctx, "tank/v", nil, false); err != nil {
				b.Fatal(err)
			}
			names := props{DefaultPropertyPrefix}
			for i := range n / 2 {
				id := fmt.Sprintf("pvc-%06d", i)
				own := map[string]string{names.managed(): "true", names.volumeName(): id, names.success(): "true"}
				if err := z.CreateFilesystem(ctx, "tank/v/"+id, own, false); err != nil {
					b.Fatal(err)
				}
				if err := z.Snapshot(ctx, "tank/v/"+id+"@s", map[string]string{
					names.managed(): "true", names.snapshotName(): "s-" + id, names.snapshotSource(): id,
				}); err != nil {
					b.Fatal(err)
				}
			}
			ep, err := New(z, Config{Parent: "tank/v", Now: time.Now})
			if err != nil {
				b.Fatal(err)
			}
			t, err := ep.scan(ctx, "")
			if err != nil {
				b.Fatal(err)
			}
			if got := t.Len(); got != n {
				b.Fatalf("scan read %d rows, want %d", got, n)
			}
			rec.r = replay(rec.last)
			b.SetBytes(int64(len(rec.last)))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := ep.scan(ctx, ""); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
