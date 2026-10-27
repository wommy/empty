package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/zfs"
)

// `zfs-endpoint reconcile` dry-runs by default and prints the plan and its
// digest; it applies only with -apply and the digest of a dry run, and
// the endpoint refuses a digest that no longer matches.
func TestReconcileSubcommand(t *testing.T) {
	t.Parallel()
	d, sim := simDeps(t)
	z := zfs.New(sim)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// two hours ago: an abandoned create (uncommitted) and a committed volume
	old := time.Now().Add(-2 * time.Hour).Unix()
	sim.Clock = func() int64 { return old }
	mk := func(id string, committed bool) {
		p := map[string]string{"zfs-endpoint:managed_resource": "true", "zfs-endpoint:csi_volume_name": id}
		if committed {
			p["zfs-endpoint:provision_success"] = "true"
		}
		if err := z.CreateFilesystem(ctx, "tank/k8s/"+id, p, false); err != nil {
			t.Fatal(err)
		}
	}
	mk("abandoned", false)
	mk("kept", true)

	dir := t.TempDir()
	sock := filepath.Join(dir, "sock", "endpoint.sock")
	admin := filepath.Join(dir, "admin", "endpoint.sock")
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-parent", "tank/k8s", "-listen", "unix://" + sock, "-admin-listen", "unix://" + admin}, d)
	}()
	waitSocket(t, admin, done)

	cli := func(args ...string) (string, error) {
		var out bytes.Buffer
		dd := d
		dd.stdout = &out
		err := run(ctx, append([]string{"reconcile", "-admin-socket", "unix://" + admin}, args...), dd)
		return out.String(), err
	}
	out, err := cli()
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	m := regexp.MustCompile(`(?m)^plan_digest: (\S+)$`).FindStringSubmatch(out)
	if m == nil || !strings.Contains(out, "destroy abandoned: ") || !strings.Contains(out, "(1 committed volumes: ") {
		t.Fatalf("dry run output:\n%s", out)
	}
	if ok, _ := z.Exists(ctx, "tank/k8s/abandoned"); !ok {
		t.Fatal("a dry run destroyed the abandoned create")
	}
	if _, err := cli("-apply"); exitCode(err) != exitConfig {
		t.Fatalf("-apply without -digest: %v, want exit %d", err, exitConfig)
	}
	if _, err := cli("-apply", "-digest", "not-the-digest"); err == nil || !strings.Contains(err.Error(), "Aborted") {
		t.Fatalf("-apply with a stale digest: %v, want Aborted", err)
	}
	if out, err := cli("-apply", "-digest", m[1]); err != nil || !strings.Contains(out, "actions applied: 1") {
		t.Fatalf("-apply -digest %s: %v\n%s", m[1], err, out)
	}
	if ok, _ := z.Exists(ctx, "tank/k8s/abandoned"); ok {
		t.Fatal("applied, but the abandoned create is still there")
	}
	if ok, _ := z.Exists(ctx, "tank/k8s/kept"); !ok {
		t.Fatal("an empty desired set collected a committed volume")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestReconcileSubcommandRefusals(t *testing.T) {
	t.Parallel()
	f := filepath.Join(t.TempDir(), "ids")
	if err := os.WriteFile(f, []byte("# kept\npvc-a\n\n pvc-b \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if ids, err := readDesired(f); err != nil || strings.Join(ids, ",") != "pvc-a,pvc-b" {
		t.Fatalf("readDesired: %q %v", ids, err)
	}
	for _, args := range [][]string{
		{},
		{"-admin-socket", "127.0.0.1:1"},
		{"-parent", "tank/k8s", "-apply"},
		{"-parent", "tank/k8s", "-digest", "x"},
		{"-parent", "tank/k8s", "-min-age", "90.5s"},
		{"-parent", "tank/k8s", "-max-deletions", "3", "-unlimited-deletions"},
		{"-parent", "tank/k8s", "extra"},
		{"-parent", "tank/k8s", "-desired-file", filepath.Join(t.TempDir(), "missing")},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), append([]string{"reconcile"}, args...), deps{stdout: &out}); exitCode(err) != exitConfig {
			t.Errorf("reconcile %q: %v, want exit %d", args, err, exitConfig)
		}
	}
}
