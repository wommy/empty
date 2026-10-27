package deploy

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The shipped reconcile timer only reports: its service dry-runs
// `zfs-endpoint reconcile` for the instance's parent with an empty desired
// set, and never applies (ADR 0001 D3: collecting needs an operator who
// read the plan).
func TestReconcileTimerIsReportOnly(t *testing.T) {
	t.Parallel()
	svc, _ := directives(t, filepath.Join("systemd", "zfs-endpoint-reconcile@.service"))
	if got := svc["Type"]; !slices.Equal(got, []string{"oneshot"}) {
		t.Errorf("Type=%q, want oneshot", got)
	}
	if len(svc["ExecStart"]) != 1 {
		t.Fatalf("ExecStart=%q, want one", svc["ExecStart"])
	}
	args := strings.Fields(svc["ExecStart"][0])
	if len(args) < 2 || filepath.Base(args[0]) != "zfs-endpoint" || args[1] != "reconcile" {
		t.Fatalf("ExecStart=%q, want zfs-endpoint reconcile", args)
	}
	if v, ok := flagValue(args, "-parent"); !ok || v != "%I" {
		t.Errorf("-parent %q, want %%I (the endpoint unit's instance)", v)
	}
	for _, a := range args {
		for _, never := range []string{"-apply", "-digest", "-desired", "-desired-file", "-unlimited-deletions", "-admin-socket"} {
			if a == never || strings.HasPrefix(a, never+"=") {
				t.Errorf("ExecStart passes %s: the timer only reports, against the unit's own admin socket", a)
			}
		}
	}
	tmr, _ := directives(t, filepath.Join("systemd", "zfs-endpoint-reconcile@.timer"))
	if len(tmr["OnCalendar"]) != 1 {
		t.Errorf("timer OnCalendar=%q, want one schedule", tmr["OnCalendar"])
	}
	if u := tmr["Unit"]; u != nil {
		t.Errorf("timer Unit=%q: it must start its own zfs-endpoint-reconcile@ instance", u)
	}
}
