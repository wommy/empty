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

// The rpc line is INFO for OK, WARN for a failed call, ERROR for
// INTERNAL, and names the ids the request carries.
func TestRPCLineLevelAndIDs(t *testing.T) {
	buf, log := logTo(t)
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	ctx := context.Background()
	if err := z.CreateFilesystem(ctx, "tank/vols", nil, false); err != nil {
		t.Fatal(err)
	}
	// someone else's dataset at a volume's name
	if err := z.CreateFilesystem(ctx, "tank/vols/pvc-foreign", nil, false); err != nil {
		t.Fatal(err)
	}
	c := daemon(t, zfs.Audit(sim, log), log)
	if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "pvc-ok", CapacityBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DeleteVolume(ctx, &pb.DeleteVolumeRequest{VolumeId: "pvc-foreign"}); err == nil {
		t.Fatal("deleted a dataset the endpoint does not own")
	}
	sim.Fail = func(args []string) error {
		if args[0] == "snapshot" || args[0] == "clone" {
			return &zfs.ExitError{Code: 1, Stderr: "something no table knows\n"}
		}
		return nil
	}
	if _, err := c.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: "pvc-bad", CapacityBytes: 1 << 20, Source: &pb.CreateVolumeRequest_SourceVolumeId{SourceVolumeId: "pvc-ok"}}); err == nil {
		t.Fatal("an unclassified create failure succeeded")
	}
	logs := buf.String()
	for _, want := range []string{
		`"level":"INFO","msg":"rpc","method":"/zfsendpoint.v1.EndpointService/CreateVolume","code":"OK","duration":`,
		`"name":"pvc-ok","peer"`,
		`"level":"WARN","msg":"rpc","method":"/zfsendpoint.v1.EndpointService/DeleteVolume","code":"FailedPrecondition"`,
		`"volume_id":"pvc-foreign"`,
		`"level":"ERROR","msg":"rpc","method":"/zfsendpoint.v1.EndpointService/CreateVolume","code":"Internal"`,
		`"name":"pvc-bad","source_volume_id":"pvc-ok"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("log lacks %s", want)
		}
	}
	if t.Failed() {
		t.Log(logs)
	}
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
