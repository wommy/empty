package server_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// logTo makes log the default logger for the test (the handlers log
// through slog's default); serial tests only.
func logTo(t *testing.T) (*syncBuffer, *slog.Logger) {
	t.Helper()
	var buf syncBuffer
	log := slog.New(server.LogHandler(slog.NewJSONHandler(&buf, nil)))
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf, log
}

// A Reconcile logs one JSON "reconcile result" line with its counts, the
// journald metric ADR 0006 names.
func TestReconcileResultLine(t *testing.T) {
	buf, log := logTo(t)
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	ctx := context.Background()
	if err := z.CreateFilesystem(ctx, "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	sim.Clock = func() int64 { return time.Now().Add(-2 * time.Hour).Unix() }
	if err := z.CreateFilesystem(ctx, "tank/vols/abandoned", map[string]string{
		"zfs-endpoint:managed_resource": "true", "zfs-endpoint:csi_volume_name": "abandoned",
	}, false); err != nil {
		t.Fatal(err)
	}
	c := daemon(t, sim, log)
	if _, err := c.Reconcile(ctx, &pb.ReconcileRequest{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	want := `"msg":"reconcile result","dry_run":true,"actions":1,"uncommitted":1,"undesired":0,"orphan_clone_sources":0,"exposures":0,"findings":1,`
	if logs := buf.String(); !strings.Contains(logs, want) {
		t.Fatalf("log lacks %s:\n%s", want, logs)
	}
}
