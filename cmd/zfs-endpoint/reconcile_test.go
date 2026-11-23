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

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
)

// The report ends with the undesired count against the cap and a
// summary: line of counts; -fail-on-findings exits exitFindings when it
// counts a finding, and 0 when it counts none (young rows are routine).
func TestReconcileReportSummary(t *testing.T) {
	t.Parallel()
	d, sim := simDeps(t)
	z := zfs.New(sim)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mk := func(id string, committed bool, extra map[string]string) {
		p := map[string]string{"zfs-endpoint:managed_resource": "true", "zfs-endpoint:csi_volume_name": id}
		if committed {
			// pinned, as CreateVolume commits it: no drift to report
			p["zfs-endpoint:provision_success"], p["mountpoint"] = "true", "/tank/k8s/"+id
		}
		for k, v := range extra {
			p[k] = v
		}
		if err := z.CreateFilesystem(ctx, "tank/k8s/"+id, p, false); err != nil {
			t.Fatal(err)
		}
	}
	// young: in flight, no finding
	mk("young", false, nil)
	sim.Clock = func() int64 { return time.Now().Add(-2 * time.Hour).Unix() }
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin", "endpoint.sock")
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-parent", "tank/k8s", "-listen", "unix://" + filepath.Join(dir, "sock", "endpoint.sock"), "-admin-listen", "unix://" + admin}, d)
	}()
	waitSocket(t, admin, done)
	cli := func(args ...string) (string, error) {
		var out bytes.Buffer
		dd := d
		dd.stdout = &out
		err := run(ctx, append([]string{"reconcile", "-admin-socket", "unix://" + admin}, args...), dd)
		return out.String(), err
	}
	out, err := cli("-fail-on-findings")
	if err != nil || !strings.Contains(out, "undesired: 0 (max 10)\n") ||
		!strings.Contains(out, "summary: uncommitted=0 undesired=0 orphan_clone_sources=0 exposures=0 findings=0 skipped=1 skipped_too_young=1\n") {
		t.Fatalf("a report of a young row: %v\n%s", err, out)
	}

	// abandoned and still exported; three committed volumes
	mk("abandoned", false, map[string]string{"sharenfs": "rw=10.0.0.1/32"})
	mk("kept", true, nil)
	mk("drop1", true, nil)
	mk("drop2", true, nil)
	out, err = cli("-fail-on-findings")
	if exitCode(err) != exitFindings || !strings.Contains(out,
		"summary: uncommitted=1 undesired=0 orphan_clone_sources=0 exposures=1 findings=2 skipped=5 skipped_empty_desired=3 skipped_too_young=1 skipped_uncommitted_exported=1\n") {
		t.Fatalf("a report with findings: %v (exit %d, want %d)\n%s", err, exitCode(err), exitFindings, out)
	}
	if !strings.Contains(out, "  abandoned: "+endpoint.SkipUncommittedExported+"\n") {
		t.Errorf("the exported abandoned create is not listed:\n%s", out)
	}
	// without the flag, the same report exits 0
	if _, err := cli(); err != nil {
		t.Errorf("a report without -fail-on-findings: %v", err)
	}
	// a desired set past the cap: the dry run says the apply would be refused
	out, err = cli("-desired", "kept", "-max-deletions", "1")
	if err != nil || !strings.Contains(out, "undesired: 2 (max 1: apply would be refused)\n") {
		t.Fatalf("a dry run past -max-deletions: %v\n%s", err, out)
	}
	if out, _ := cli("-desired", "kept", "-unlimited-deletions"); !strings.Contains(out, "undesired: 2 (no max)\n") {
		t.Errorf("-unlimited-deletions:\n%s", out)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

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

// An applied run in which a group failed exits non-zero, after printing
// what ran and the failed target: a wrapper must not read a partial
// collection as done.
func TestReconcileSubcommandApplyFailure(t *testing.T) {
	t.Parallel()
	d, sim := simDeps(t)
	z := zfs.New(sim)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := time.Now().Add(-2 * time.Hour).Unix()
	sim.Clock = func() int64 { return old }
	for _, id := range []string{"gone", "stuck"} {
		if err := z.CreateFilesystem(ctx, "tank/k8s/"+id, map[string]string{"zfs-endpoint:managed_resource": "true", "zfs-endpoint:csi_volume_name": id}, false); err != nil {
			t.Fatal(err)
		}
	}
	sim.Fail = func(args []string) error {
		if args[0] == "destroy" && args[len(args)-1] == "tank/k8s/stuck" {
			return &zfs.ExitError{Code: 1, Stderr: "cannot destroy 'tank/k8s/stuck': dataset is busy\n"}
		}
		return nil
	}
	dir := t.TempDir()
	admin := filepath.Join(dir, "admin", "endpoint.sock")
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"-parent", "tank/k8s", "-listen", "unix://" + filepath.Join(dir, "sock", "endpoint.sock"), "-admin-listen", "unix://" + admin}, d)
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
	m := regexp.MustCompile(`(?m)^plan_digest: (\S+)$`).FindStringSubmatch(out)
	if err != nil || m == nil {
		t.Fatalf("dry run: %v\n%s", err, out)
	}
	out, err = cli("-apply", "-digest", m[1])
	if err == nil || exitCode(err) == exitConfig || !strings.Contains(out, "destroy gone: ") || !strings.Contains(out, "stuck: failed: ") {
		t.Fatalf("-apply with a failed destroy: %v, want a non-config error after the plan\n%s", err, out)
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
		{"-parent", "tank/k8s", "-max-deletions", "4294967296"},
		{"-parent", "tank/k8s", "extra"},
		{"-parent", "tank/k8s", "-desired-file", filepath.Join(t.TempDir(), "missing")},
	} {
		var out bytes.Buffer
		if err := run(context.Background(), append([]string{"reconcile"}, args...), deps{stdout: &out}); exitCode(err) != exitConfig {
			t.Errorf("reconcile %q: %v, want exit %d", args, err, exitConfig)
		}
	}
}
