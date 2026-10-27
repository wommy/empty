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
	// a report with findings fails the unit (exit 3), so it is seen
	if !slices.Contains(args, "-fail-on-findings") {
		t.Errorf("ExecStart=%q lacks -fail-on-findings: a report with findings would exit 0 unseen", args)
	}
	for _, a := range timerForbidden(args) {
		t.Errorf("ExecStart passes %s: the timer only reports, against the unit's own admin socket", a)
	}
	// Go's flag package takes --apply and -apply=true as -apply
	for _, a := range []string{"-apply", "--apply", "-apply=true", "--apply=1", "--digest=x", "--digest", "--admin-socket=unix:///x"} {
		if got := timerForbidden([]string{"zfs-endpoint", "reconcile", a}); len(got) != 1 {
			t.Errorf("timerForbidden misses %s", a)
		}
	}
	if got := timerForbidden([]string{"zfs-endpoint", "reconcile", "-parent", "%I", "-timeout", "5m", "-fail-on-findings"}); got != nil {
		t.Errorf("timerForbidden(the report flags) = %q", got)
	}
	// a client of one unix socket, mounting nothing: unlike the daemon it
	// can take a mount namespace, and no capability (root owns the socket)
	for k, want := range map[string]string{
		"ProtectSystem": "strict", "ProtectHome": "yes", "PrivateTmp": "yes", "PrivateDevices": "yes",
		"ProtectKernelTunables": "yes", "ProtectKernelModules": "yes", "ProtectKernelLogs": "yes",
		"ProtectControlGroups": "yes", "ProtectClock": "yes", "ProtectHostname": "yes",
		"RestrictNamespaces": "yes", "CapabilityBoundingSet": "", "SystemCallFilter": "@system-service",
		"RestrictAddressFamilies": "AF_UNIX", "NoNewPrivileges": "yes",
	} {
		if got := svc[k]; !slices.Equal(got, []string{want}) {
			t.Errorf("%s=%q, want %q", k, got, want)
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

// timerForbidden returns the arguments of a reconcile command line that
// apply, name a desired set or move the socket, in any spelling Go's flag
// package accepts (-x, --x, -x=v, --x=v).
func timerForbidden(args []string) (bad []string) {
	for _, a := range args {
		f := a
		if strings.HasPrefix(f, "--") {
			f = f[1:]
		}
		f, _, _ = strings.Cut(f, "=")
		if slices.Contains([]string{"-apply", "-digest", "-desired", "-desired-file", "-unlimited-deletions", "-admin-socket"}, f) {
			bad = append(bad, a)
		}
	}
	return bad
}
