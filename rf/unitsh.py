import re
p='ci/real-zfs-unit.sh'
s=open(p).read()
rep=[
('''#     (SO_PEERCRED, ADR 0001 v2 D2).
''','''#     (SO_PEERCRED, ADR 0001 v2 D2);
#   - the reconcile timer's service, hardening and all, runs its dry run
#     against that admin socket and logs no EPERM.
'''),
('''#   <dir> holds zfs-endpoint, unitprobe (ci/unitprobe), zfs-endpoint@.service
#   and hardening.conf
''','''#   <dir> holds zfs-endpoint, unitprobe (ci/unitprobe), zfs-endpoint@.service,
#   hardening.conf and zfs-endpoint-reconcile@.service
'''),
('''inst=zfs-endpoint@$esc.service
''','''inst=zfs-endpoint@$esc.service
rinst=zfs-endpoint-reconcile@$esc.service
'''),
('''  systemctl reset-failed "$inst" >/dev/null 2>&1
  zpool destroy''','''  systemctl reset-failed "$inst" "$rinst" >/dev/null 2>&1
  zpool destroy'''),
('''  install -m 0644 "$src/zfs-endpoint@.service" "$unitdir/zfs-endpoint@.service" ||''','''  install -m 0644 "$src/zfs-endpoint@.service" "$unitdir/zfs-endpoint@.service" &&
  install -m 0644 "$src/zfs-endpoint-reconcile@.service" "$unitdir/zfs-endpoint-reconcile@.service" ||'''),
('''# eperm: lines of the instance's journal since $since that say EPERM
eperm() {
  journalctl -u "$inst" --since''','''# reconcile_unit: the timer's service (oneshot: start waits for it, and
# fails when the dry run does) under its own hardening
reconcile_unit() {
  systemctl start "$rinst" && eperm "$rinst" && return
  echo "reconcile: $rinst failed under its hardening"
  journalctl -u "$rinst" --since "@$since" --no-pager -o cat 2>&1 | awk 'NR <= 30'
  return 1
}

# eperm [unit]: lines of its journal (the instance's) since $since that
# say EPERM
eperm() {
  journalctl -u "${1:-$inst}" --since'''),
('''  admin_checks || rc=1
  eperm ||''','''  admin_checks || rc=1
  reconcile_unit || rc=1
  eperm ||'''),
('''    verdict "PASS: served under the unit and the drop-in (${active[*]:-no settings}); admin refused uid 65534"''','''    verdict "PASS: served under the unit and the drop-in (${active[*]:-no settings}); admin refused uid 65534; reconcile unit ran"'''),
]
for a,b in rep:
    assert a in s, a
    s=s.replace(a,b,1)
open(p,'w').write(s)

p='ci/real-zfs-vm.sh'
s=open(p).read()
a='''deploy/systemd/zfs-endpoint@.service \\
  deploy/systemd/zfs-endpoint@.service.d/hardening.conf)'''
b='''deploy/systemd/zfs-endpoint@.service \\
  deploy/systemd/zfs-endpoint@.service.d/hardening.conf deploy/systemd/zfs-endpoint-reconcile@.service)'''
assert a in s
s=s.replace(a,b)
open(p,'w').write(s)

p='deploy/unitcheck_test.go'
s=open(p).read()
rep=[
('''// stubs: systemctl fails start while a drop-in carries $FAIL_SETTING
// ("always": every start), unitprobe''','''// stubs: systemctl fails start while a drop-in carries $FAIL_SETTING
// ("always": every start) or of unit $FAIL_UNIT, unitprobe'''),
('''  [ "$FAIL_SETTING" = always ] && exit 1
''','''  [ "$FAIL_SETTING" = always ] && exit 1
  [ -n "$FAIL_UNIT" ] && [ "$2" = "$FAIL_UNIT" ] && exit 1
'''),
('''		"hardening.conf":        filepath.Join("systemd", "zfs-endpoint@.service.d", "hardening.conf"),
''','''		"hardening.conf":        filepath.Join("systemd", "zfs-endpoint@.service.d", "hardening.conf"),
		"zfs-endpoint-reconcile@.service": filepath.Join("systemd", "zfs-endpoint-reconcile@.service"),
'''),
('''		"FAIL_SETTING=", "FAKE_NOBODY''','''		"FAIL_SETTING=", "FAIL_UNIT=", "FAKE_NOBODY'''),
('''		{"EPERM in the journal", []string{"FAKE_JOURNAL=cannot mount: Operation not permitted"}, 1, "unit hardening: FAIL: served, but the checks above failed"},
''','''		{"EPERM in the journal", []string{"FAKE_JOURNAL=cannot mount: Operation not permitted"}, 1, "unit hardening: FAIL: served, but the checks above failed"},
		{"the reconcile unit fails", []string{"FAIL_UNIT=zfs-endpoint-reconcile@zunit-k8s.service"}, 1, "unit hardening: FAIL: served, but the checks above failed"},
'''),
('''				tc.name == "EPERM in the journal" && !strings.Contains(out, "journal: cannot mount: Operation not permitted") {''','''				tc.name == "EPERM in the journal" && !strings.Contains(out, "journal: cannot mount: Operation not permitted") ||
				tc.name == "the reconcile unit fails" && !strings.Contains(out, "reconcile: zfs-endpoint-reconcile@zunit-k8s.service failed") {'''),
]
for a,b in rep:
    assert a in s, a
    s=s.replace(a,b,1)
open(p,'w').write(s)
