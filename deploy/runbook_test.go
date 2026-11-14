package deploy

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The README's "Runbook: when a PVC is stuck" quotes the texts an
// operator greps for. Each must be in the runbook and in the endpoint's
// sources (outside tests), so a reworded message fails here rather than
// leaving the runbook pointing at nothing.
func TestRunbookQuotesLiveMessages(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile(filepath.Join("..", "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "\n## Runbook: when a PVC is stuck\n")
	if !ok {
		t.Fatal(`README lacks "## Runbook: when a PVC is stuck"`)
	}
	runbook, _, _ := strings.Cut(rest, "\n## ")
	var src strings.Builder
	for _, dir := range []string{"../internal/endpoint", "../internal/server", "../internal/zfs", "../internal/lock", "../cmd/zfs-endpoint"} {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return err
			}
			b, err := os.ReadFile(p)
			src.Write(b)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	code := src.String()
	for _, msg := range []string{
		"operation pending on", "held for", "by request",
		"earlier ops applied; a retry converges", "resuming an earlier attempt",
		"out of space: free space on the pool first, then a retry converges",
		"waiting for a free zfs slot", "health: not serving", "health: free space below -min-free-bytes",
		"zfs exec", "zfs done", "zfs failed", "zfs slow",
		"endpoint is shutting down", "is unavailable", "no nfsd threads", "the NFS export table does not serve",
		"served beyond its export", "served beyond its client list by the NFS export table",
		"has dependent clone", "is an uncommitted create", "is not managed",
		"has held snapshot", "has snapshots the endpoint did not make", "datasets below it",
		"handler panic", "-min-free-bytes",
	} {
		if !strings.Contains(runbook, msg) {
			t.Errorf("the runbook does not quote %q", msg)
		}
		if !strings.Contains(code, msg) {
			t.Errorf("the runbook quotes %q, which no source emits", msg)
		}
	}
}
