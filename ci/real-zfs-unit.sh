#!/usr/bin/env bash
# Runs inside the real-ZFS guest, as root, called by ci/real-zfs-guest.sh:
# zfs-endpoint under its systemd unit and the hardening drop-in, as shipped
# (deploy/systemd), on a pool of its own. It checks that:
#   - the instance starts (Type=notify: READY=1) and serves: health, one
#     CreateVolume and one DeleteVolume on the csi socket;
#   - the journal holds no EPERM ("Operation not permitted") from the
#     daemon or its zfs children;
#   - the admin socket serves root (a dry-run Reconcile), refuses uid 65534
#     at connect(2) with its shipped modes, and, with every mode on its path
#     opened to others, refuses it in the daemon with PERMISSION_DENIED
#     (SO_PEERCRED, ADR 0001 v2 D2);
#   - the reconcile timer's service, hardening and all, runs its dry run
#     against that admin socket and logs no EPERM.
# When the drop-in fails, it starts the unit alone, then the unit with each
# active drop-in setting on its own, and names the settings that fail.
# The last line is the verdict: "unit hardening: PASS ..." or
# "unit hardening: FAIL: ...". Exit 0 on PASS, 1 on FAIL.
#
# usage: real-zfs-unit.sh <dir>
#   <dir> holds zfs-endpoint, unitprobe (ci/unitprobe), zfs-endpoint@.service,
#   hardening.conf and zfs-endpoint-reconcile@.service
# real-zfs-unit.sh --settings <hardening.conf>: print the drop-in's active
#   settings, one per line, and exit (deploy/deploy_test.go)
#
# UNIT_DIR, UNIT_SBIN, UNIT_BIN, UNIT_RUN, UNIT_VDEV and UNIT_POOL move what
# it installs, reads and creates (deploy/deploy_test.go runs it with stubs).
set -uo pipefail

# settings <conf>: the active Key=value lines of its [Service] section
settings() {
  awk '/^\[/ { s = ($0 == "[Service]"); next } s && /^[A-Za-z]+=/' "$1"
}
if [ "${1:-}" = --settings ]; then
  settings "${2:?hardening.conf}"
  exit
fi

src=${1:?directory with zfs-endpoint, unitprobe, the unit and the drop-in}
unitdir=${UNIT_DIR:-/etc/systemd/system}
sbin=${UNIT_SBIN:-/usr/local/sbin}
bin=${UNIT_BIN:-/usr/local/bin}
rundir=${UNIT_RUN:-/run/zfs-endpoint}
pool=${UNIT_POOL:-zunit}
vdev=${UNIT_VDEV:-/run/zfsci/$pool.vdev}
# the pool name is [a-z0-9]: systemd-escape of $pool/k8s is $pool-k8s
parent=$pool/k8s
esc=$pool-k8s
inst=zfs-endpoint@$esc.service
rinst=zfs-endpoint-reconcile@$esc.service
csi=unix://$rundir/sock/$esc/endpoint.sock
admin=unix://$rundir/admin/$esc/endpoint.sock
dropdir=$unitdir/zfs-endpoint@.service.d
t0=$EPOCHREALTIME
elapsed() { awk -v a="$t0" -v b="$EPOCHREALTIME" 'BEGIN { printf "%.2fs", b - a }'; }

verdict() { echo "unit hardening: $1 ($(elapsed))"; }
cleanup() {
  systemctl stop "$inst" >/dev/null 2>&1
  systemctl reset-failed "$inst" "$rinst" >/dev/null 2>&1
  zpool destroy -f "$pool" >/dev/null 2>&1
  rm -f "$vdev"
}
trap cleanup EXIT

install -m 0755 "$src/zfs-endpoint" "$sbin/zfs-endpoint" &&
  install -m 0755 "$src/unitprobe" "$bin/unitprobe" &&
  install -m 0644 "$src/zfs-endpoint@.service" "$unitdir/zfs-endpoint@.service" &&
  install -m 0644 "$src/zfs-endpoint-reconcile@.service" "$unitdir/zfs-endpoint-reconcile@.service" ||
  { verdict "FAIL: could not install the binaries and the unit"; exit 1; }
mkdir -p "$(dirname "$vdev")"
truncate -s 1G "$vdev" && zpool create -f "$pool" "$vdev" && zfs create "$parent" ||
  { verdict "FAIL: could not create pool $pool (not a hardening setting)"; exit 1; }

probe() { "$bin/unitprobe" "$@"; }

# attempt <label> <drop-in file or "">: start the unit with that drop-in
# (none for ""), check it serves, stop it. Returns 1 with the reason
# printed when it does not.
attempt() {
  local label=$1 conf=$2 out id rc=0
  echo "== $label"
  rm -rf "$dropdir"
  if [ -n "$conf" ]; then
    mkdir -p "$dropdir" && cp "$conf" "$dropdir/hardening.conf"
  fi
  systemctl daemon-reload || { echo "$label: systemctl daemon-reload failed"; return 1; }
  since=$(date +%s)
  if ! systemctl start "$inst"; then
    echo "$label: the instance did not start"
    systemctl status --no-pager "$inst" 2>&1 | awk 'NR <= 15'
    journalctl -u "$inst" --since "@$since" --no-pager -o cat 2>&1 | awk '{ l[NR % 30] = $0 } END { for (i = NR - 29; i <= NR; i++) if (i > 0) print l[i % 30] }'
    systemctl stop "$inst" >/dev/null 2>&1
    systemctl reset-failed "$inst" >/dev/null 2>&1
    return 1
  fi
  probe -addr "$csi" health || rc=1
  if [ $rc = 0 ] && out=$(probe -addr "$csi" create unit-check); then
    echo "$out"
    id=${out#create: OK: }
    probe -addr "$csi" delete "$id" || rc=1
  else
    echo "${out:-}"
    rc=1
  fi
  [ $rc = 0 ] || echo "$label: the instance started but did not serve"
  return $rc
}

stop() {
  systemctl stop "$inst" || echo "systemctl stop $inst failed"
}

# admin: root is served, uid 65534 is refused by the socket's modes and,
# with them opened, by the daemon
admin_checks() {
  local rc=0 as_nobody=(setpriv --reuid=65534 --regid=65534 --clear-groups)
  local sock=${admin#unix://} p modes=()
  probe -addr "$admin" reconcile || { echo "admin: root was not served"; rc=1; }
  "${as_nobody[@]}" "$bin/unitprobe" -addr "$admin" -want CONNECT reconcile ||
    { echo "admin: uid 65534 got past the shipped socket modes"; rc=1; }
  # every directory on the way, and the socket, opened to others; then
  # restored, whatever happens
  local path=("$rundir" "$rundir/admin" "$(dirname "$sock")" "$sock")
  for p in "${path[@]}"; do modes+=("$(stat -c %a "$p")") || return 1; done
  chmod o+x "${path[@]:0:3}" && chmod o+rw "$sock" || { echo "admin: could not open the modes"; rc=1; }
  "${as_nobody[@]}" "$bin/unitprobe" -addr "$admin" -want PermissionDenied reconcile ||
    { echo "admin: uid 65534 was not refused PERMISSION_DENIED with the modes opened"; rc=1; }
  for i in "${!path[@]}"; do chmod "${modes[$i]}" "${path[$i]}"; done
  return $rc
}

# reconcile_unit: the timer's service (oneshot: start waits for it, and
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
  journalctl -u "${1:-$inst}" --since "@$since" --no-pager -o cat 2>&1 |
    awk '/Operation not permitted|EPERM/ { n++; if (n <= 10) print "journal: " $0 } END { exit n > 0 }'
}

conf=$src/hardening.conf
mapfile -t active < <(settings "$conf")
echo "drop-in settings in effect: ${active[*]:-none}"
if attempt "unit + drop-in" "$conf"; then
  rc=0
  admin_checks || rc=1
  reconcile_unit || rc=1
  eperm || { echo "the journal shows EPERM under the drop-in"; rc=1; }
  stop
  if [ $rc = 0 ]; then
    verdict "PASS: served under the unit and the drop-in (${active[*]:-no settings}); admin refused uid 65534; reconcile unit ran"
    exit 0
  fi
  verdict "FAIL: served, but the checks above failed"
  exit 1
fi

# which setting: the unit alone, then each setting alone
if ! attempt "unit alone" ""; then
  verdict "FAIL: the unit fails without the drop-in too: not a hardening setting"
  exit 1
fi
stop
bad=()
one=$(mktemp)
for s in "${active[@]}"; do
  printf '[Service]\n%s\n' "$s" >"$one"
  if attempt "unit + $s" "$one"; then stop; else bad+=("$s"); fi
done
rm -f "$one"
if [ ${#bad[@]} -gt 0 ]; then
  verdict "FAIL: drop-in settings that stop the endpoint serving: ${bad[*]}"
else
  verdict "FAIL: the drop-in fails as a whole, but no setting alone does"
fi
exit 1
