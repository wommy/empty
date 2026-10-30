package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// unitCheck is the guest-side check of the unit and its hardening drop-in
// (ci/real-zfs-guest.sh runs it on the real-ZFS VM).
var unitCheck = filepath.Join("..", "ci", "real-zfs-unit.sh")

// The check reports on exactly the drop-in's settings in effect: a setting
// turned on is one it can name when the endpoint stops serving.
func TestUnitCheckReadsTheDropIn(t *testing.T) {
	t.Parallel()
	conf := filepath.Join("systemd", "zfs-endpoint@.service.d", "hardening.conf")
	out, err := exec.Command("bash", unitCheck, "--settings", conf).Output()
	if err != nil {
		t.Fatal(err)
	}
	var got, want []string
	for l := range strings.Lines(string(out)) {
		k, _, _ := strings.Cut(strings.TrimSpace(l), "=")
		got = append(got, k)
	}
	active, _ := directives(t, conf)
	for k := range active {
		want = append(want, k)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("the check reads %q, the drop-in has %q in effect", got, want)
	}
}

// stubs: systemctl fails start while a drop-in carries $FAIL_SETTING
// ("always": every start) or of unit $FAIL_UNIT, unitprobe answers as the endpoint would for
// the socket modes the check sets ($FAKE_NOBODY: what uid 65534 gets past
// them), setpriv marks the call as uid 65534, journalctl prints
// $FAKE_JOURNAL.
var unitCheckStubs = map[string]string{
	"systemctl": `case $1 in start)
  [ "$FAIL_SETTING" = always ] && exit 1
  [ -n "$FAIL_UNIT" ] && [ "$2" = "$FAIL_UNIT" ] && exit 1
  [ -n "$FAIL_SETTING" ] && cat "$UNIT_DIR"/zfs-endpoint@.service.d/*.conf 2>/dev/null | awk -v s="$FAIL_SETTING=" 'index($0, s) == 1 { f = 1 } END { exit !f }' && exit 1
esac
exit 0`,
	"journalctl": `printf '%s\n' "$FAKE_JOURNAL"`,
	"zpool":      `exit 0`,
	"zfs":        `exit 0`,
	"setpriv":    `while [ "${1#--}" != "$1" ]; do shift; done; FAKE_UID=65534 exec "$@"`,
	"unitprobe": `want=OK
while :; do case $1 in -want) want=$2; shift 2 ;; -addr) addr=$2; shift 2 ;; *) break ;; esac; done
got=OK
if [ "${FAKE_UID:-0}" = 65534 ]; then
  if [ "$(stat -c %a "${addr#unix://}")" = 600 ]; then got=CONNECT; else got=$FAKE_NOBODY; fi
fi
if [ "$1" = create ] && [ "$got" = OK ]; then echo "create: OK: unit-check"; exit 0; fi
echo "$1: $got"
[ "$got" = "$want" ]`,
}

// runUnitCheck runs the check against the stubs with env added, and
// returns its output, its last line and its exit code.
func runUnitCheck(t *testing.T, env ...string) (out, last string, code int) {
	t.Helper()
	dir := t.TempDir()
	stubs, src, run := filepath.Join(dir, "stubs"), filepath.Join(dir, "src"), filepath.Join(dir, "run")
	for _, d := range []string{stubs, src, filepath.Join(dir, "unit"), filepath.Join(dir, "sbin"), filepath.Join(dir, "bin"), filepath.Join(run, "admin", "zunit-k8s")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range unitCheckStubs {
		if err := os.WriteFile(filepath.Join(stubs, name), []byte("#!/usr/bin/env bash\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, from := range map[string]string{
		"zfs-endpoint@.service":           filepath.Join("systemd", "zfs-endpoint@.service"),
		"hardening.conf":                  filepath.Join("systemd", "zfs-endpoint@.service.d", "hardening.conf"),
		"zfs-endpoint-reconcile@.service": filepath.Join("systemd", "zfs-endpoint-reconcile@.service"),
		"zfs-endpoint":                    filepath.Join(stubs, "zpool"),
		"unitprobe":                       filepath.Join(stubs, "unitprobe"),
	} {
		b, err := os.ReadFile(from)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, name), b, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(run, "admin", "zunit-k8s", "endpoint.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", unitCheck, src)
	cmd.Env = append(os.Environ(), "PATH="+stubs+":"+os.Getenv("PATH"),
		"UNIT_DIR="+filepath.Join(dir, "unit"), "UNIT_SBIN="+filepath.Join(dir, "sbin"), "UNIT_BIN="+filepath.Join(dir, "bin"),
		"UNIT_RUN="+run, "UNIT_VDEV="+filepath.Join(dir, "zunit.vdev"),
		"FAIL_SETTING=", "FAIL_UNIT=", "FAKE_NOBODY=PermissionDenied", "FAKE_JOURNAL=")
	cmd.Env = append(cmd.Env, env...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	out = strings.TrimSpace(string(b))
	return out, out[strings.LastIndex(out, "\n")+1:], code
}

// The first VM run must say what failed: the verdict is the last line,
// and a drop-in setting that stops the endpoint serving is named, alone.
func TestUnitCheckNamesTheFailingSetting(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		env  []string
		code int
		want string
	}{
		{"serves", nil, 0, "unit hardening: PASS: served under the unit and the drop-in"},
		{"one setting breaks it", []string{"FAIL_SETTING=ProtectHostname"}, 1,
			"unit hardening: FAIL: drop-in settings that stop the endpoint serving: ProtectHostname=yes ("},
		{"the unit itself fails", []string{"FAIL_SETTING=always"}, 1, "unit hardening: FAIL: the unit fails without the drop-in too"},
		{"uid 65534 is served", []string{"FAKE_NOBODY=OK"}, 1, "unit hardening: FAIL: served, but the checks above failed"},
		{"EPERM in the journal", []string{"FAKE_JOURNAL=cannot mount: Operation not permitted"}, 1, "unit hardening: FAIL: served, but the checks above failed"},
		{"the reconcile unit fails", []string{"FAIL_UNIT=zfs-endpoint-reconcile@zunit-k8s.service"}, 1, "unit hardening: FAIL: served, but the checks above failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out, last, code := runUnitCheck(t, tc.env...)
			t.Log(out)
			if code != tc.code || !strings.HasPrefix(last, tc.want) {
				t.Errorf("exit %d, last line %q; want %d, %q\n%s", code, last, tc.code, tc.want, out)
			}
			if tc.name == "uid 65534 is served" && !strings.Contains(out, "uid 65534 was not refused PERMISSION_DENIED") ||
				tc.name == "EPERM in the journal" && !strings.Contains(out, "journal: cannot mount: Operation not permitted") ||
				tc.name == "the reconcile unit fails" && !strings.Contains(out, "reconcile: zfs-endpoint-reconcile@zunit-k8s.service failed") {
				t.Errorf("the reason is not in the output:\n%s", out)
			}
		})
	}
}
