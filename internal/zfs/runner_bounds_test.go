package zfs_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// H-09: a failed command's stderr goes into the error that is returned to
// the caller and logged (a destroy refused for its clones lists every
// one). Only its head is kept, which is where zfs puts the message that
// classify reads.
func TestStderrIsBounded(t *testing.T) {
	t.Parallel()
	_, err := zfs.LocalRunner{}.Run(context.Background(), "sh", "-c",
		`echo "cannot destroy 'tank/v': filesystem has dependent clones" >&2; head -c 3000000 /dev/zero | tr '\0' x >&2; exit 1`)
	var ee *zfs.ExitError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %T %v", err, err)
	}
	if len(ee.Stderr) > 16<<10 || len(ee.Error()) > 16<<10 {
		t.Errorf("ExitError carries %d bytes of stderr (Error() %d)", len(ee.Stderr), len(ee.Error()))
	}
	if !strings.HasPrefix(ee.Stderr, "cannot destroy 'tank/v': filesystem has dependent clones\n") {
		t.Errorf("stderr head lost: %.80q", ee.Stderr)
	}
	if !strings.Contains(ee.Stderr, "truncated") {
		t.Errorf("a cut stderr does not say so: ...%q", ee.Stderr[max(0, len(ee.Stderr)-60):])
	}
	if !errors.Is(zfs.New(errRunner{ee}).Destroy(context.Background(), "tank/v", zfs.DestroyOptions{}), zfs.ErrHasClones) {
		t.Error("a cut stderr no longer classifies")
	}

	// a short stderr is kept whole
	_, err = zfs.LocalRunner{}.Run(context.Background(), "sh", "-c", `printf 'short\n' >&2; exit 1`)
	if !errors.As(err, &ee) || ee.Stderr != "short\n" {
		t.Errorf("short stderr: %v", err)
	}
}

type errRunner struct{ err error }

func (r errRunner) Run(context.Context, string, ...string) ([]byte, error) { return nil, r.err }
