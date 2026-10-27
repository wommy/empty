p='deploy/reconcile_timer_test.go'
s=open(p).read()
old='''	for _, a := range args {
		for _, never := range []string{"-apply", "-digest", "-desired", "-desired-file", "-unlimited-deletions", "-admin-socket"} {
			if a == never || strings.HasPrefix(a, never+"=") {
				t.Errorf("ExecStart passes %s: the timer only reports, against the unit's own admin socket", a)
			}
		}
	}
'''
new='''	for _, a := range timerForbidden(args) {
		t.Errorf("ExecStart passes %s: the timer only reports, against the unit's own admin socket", a)
	}
	// Go's flag package takes --apply and -apply=true as -apply
	for _, a := range []string{"-apply", "--apply", "-apply=true", "--apply=1", "--digest=x", "--digest", "--admin-socket=unix:///x"} {
		if got := timerForbidden([]string{"zfs-endpoint", "reconcile", a}); len(got) != 1 {
			t.Errorf("timerForbidden misses %s", a)
		}
	}
	if got := timerForbidden([]string{"zfs-endpoint", "reconcile", "-parent", "%I", "-timeout", "5m"}); got != nil {
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
'''
assert old in s
s=s.replace(old,new)
s+='''
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
'''
open(p,'w').write(s)
