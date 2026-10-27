package zfs_test

import (
	"context"
	"errors"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// countRunner counts commands and succeeds.
type countRunner struct{ n int }

func (r *countRunner) Run(context.Context, string, ...string) ([]byte, error) {
	r.n++
	return nil, nil
}

// F11: a value the endpoint writes must read back byte for byte from the
// JSON scan, in any locale zfs runs in. Only printable ASCII does: the
// printer drops code points above U+FFFF, and under LC_ALL=C it stops at
// the first byte >= 0x80, failing the scan of the whole parent with exit 0.
// Every write path refuses anything else before zfs runs.
func TestCheckPropsRefusesNonPrintable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	writes := map[string]func(c *zfs.Client, props map[string]string) error{
		"create": func(c *zfs.Client, p map[string]string) error { return c.CreateFilesystem(ctx, "tank/a", p, false) },
		"create -V": func(c *zfs.Client, p map[string]string) error {
			return c.CreateVolume(ctx, "tank/v", 1<<20, false, p)
		},
		"snapshot": func(c *zfs.Client, p map[string]string) error { return c.Snapshot(ctx, "tank/a@s", p) },
		"clone":    func(c *zfs.Client, p map[string]string) error { return c.Clone(ctx, "tank/a@s", "tank/b", p) },
		"set":      func(c *zfs.Client, p map[string]string) error { return c.Set(ctx, "tank/a", p) },
		"set -u":   func(c *zfs.Client, p map[string]string) error { return c.SetNoMount(ctx, "tank/a", p) },
	}
	for name, write := range writes {
		for _, v := range []string{"a\nb", "a\tb", "a\rb", "\x00", "\x01", "del\x7f", "café", "😀", "\xff"} {
			r := &countRunner{}
			err := write(zfs.New(r), map[string]string{"p:x": v})
			var ze *zfs.Error
			if !errors.As(err, &ze) || ze.Kind != zfs.KindInvalidProperty || r.n != 0 {
				t.Errorf("%s with value %q: err %v, %d commands run", name, v, err, r.n)
			}
		}
		r := &countRunner{}
		if err := write(zfs.New(r), map[string]string{"p:x": ` !"#$%&'()*+,-./09:;<=>?@AZ[\]^_` + "`az{|}~", "sharenfs": "rw=10.0.0.0/16"}); err != nil || r.n != 1 {
			t.Errorf("%s with printable ASCII: err %v, %d commands run", name, err, r.n)
		}
	}
}
