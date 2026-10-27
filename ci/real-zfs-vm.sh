#!/usr/bin/env bash
# Run the endpoint contract tests against real ZFS inside an Ubuntu 26.10
# guest booted under KVM. GitHub has no runner for a non-LTS Ubuntu, and ZFS
# lives in the kernel, so a container would test the host's ZFS instead.
#
# The guest is a prebaked bundle (ci/vm/bake.sh: OpenZFS and nfs-utils
# installed, cloud-init off, kernel and initrd for direct boot, an extracted
# qemu), so a run is: boot it in the background, build the test binaries
# while it boots, let the guest run them in parallel shards, check the logs.
# The bundle is cached and therefore trusted for nothing: every boot records
# `zfs version`, the module's own version and the kernel, and `wait` asserts
# them here, on the host, from the files the guest wrote.
#
# usage:
#   ci/real-zfs-vm.sh boot <bundle>           start the guest in the background
#   ci/real-zfs-vm.sh build                   build the test binaries into the share
#   ci/real-zfs-vm.sh wait <openzfs-minor>    wait for the guest, assert, check skips
#   ci/real-zfs-vm.sh smoke <openzfs-minor> <bundle>   bake self-test (no Go)
#
# The guest and the host share $work/share over 9p: the host puts
# guest-run.sh, the test weights and bin/*.test there and touches `ready`;
# the guest (zfsci-boot, baked) runs guest-run.sh and writes out/.
set -euo pipefail

cmd=${1:?boot, build, wait or smoke}
work=${RUNNER_TEMP:-/tmp}/zfsvm
share=$work/share
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
cd "$(git rev-parse --show-toplevel)"

since() { awk -v a="$1" -v b="$(date +%s.%N)" 'BEGIN { printf "%.1f", b - a }'; }
prov() { sed -n "s/^$1=//p" "$share/out/provenance" 2>/dev/null | head -n 1; }

diag() {
  echo "--- guest console (tail) ---"
  tail -n 80 "$work/console.log" 2>/dev/null || true
  echo "--- qemu ---"
  cat "$work/qemu.log" 2>/dev/null || true
  echo "--- guest-run ---"
  tail -n 80 "$share/out/guest.log" 2>/dev/null || true
  echo "--- shards ---"
  cat "$share/out/shards.txt" 2>/dev/null || true
}

boot() {
  local bundle=${1:?bundle directory}
  local f
  for f in base.qcow2 vmlinuz initrd.img cmdline provenance; do
    [ -s "$bundle/$f" ] || { echo "::error::VM bundle incomplete: no $f in $bundle"; exit 1; }
  done
  rm -rf "$work"
  mkdir -p "$share/bin" "$share/out"
  cp ci/vm/guest-run.sh ci/vm/test-weights.txt "$share/"
  echo "$bundle" >"$work/bundle"

  # how old is the bake? A stale-but-2.4 image passes the version assert,
  # so its age is reported, and a month-old one fails (the nightly bake is
  # broken)
  local baked age
  baked=$(sed -n 's/^baked_at=//p' "$bundle/provenance")
  age=$((($(date -u +%s) - ${baked:-0}) / 86400))
  if [ "$age" -gt 30 ]; then
    echo "::error::the VM bundle is $age days old: the nightly bake (vm-image workflow) is failing"
    exit 1
  elif [ "$age" -gt 7 ]; then
    echo "::warning::the VM bundle is $age days old: check the nightly bake (vm-image workflow)"
  fi

  # the bundle's own qemu, run in place; the apt fallback costs ~13s and
  # happens when the runner image moved under the extracted libraries
  local q=$bundle/qemu
  local qemu=("$q/usr/bin/qemu-system-x86_64" -L "$q/usr/share/qemu" -L "$q/usr/share/seabios")
  local envs=("LD_LIBRARY_PATH=$q/usr/lib/x86_64-linux-gnu" "QEMU_MODULE_DIR=$q/usr/lib/x86_64-linux-gnu/qemu")
  if ! env "${envs[@]}" "${qemu[@]}" --version >/dev/null 2>&1; then
    echo "::warning::the bundle's qemu does not run on this runner image; installing qemu from apt"
    sudo apt-get update -qq
    sudo apt-get install -y -qq --no-install-recommends qemu-system-x86 >/dev/null
    qemu=(qemu-system-x86_64)
    envs=()
  fi

  # -nodefaults and the fixed MAC match the bake boot, so the guest's
  # network config (written by cloud-init at bake time) still applies
  setsid nohup env "${envs[@]}" "${qemu[@]}" \
    -machine q35,accel=kvm -cpu host -smp "$(nproc)" -m 6144 \
    -nodefaults -display none -no-reboot \
    -kernel "$bundle/vmlinuz" -initrd "$bundle/initrd.img" \
    -append "$(cat "$bundle/cmdline") console=ttyS0 zfsci=1" \
    -drive file="$bundle/base.qcow2",if=virtio,format=qcow2,snapshot=on,cache=unsafe \
    -virtfs local,path="$share",mount_tag=ci,security_model=none,id=ci \
    -netdev user,id=n0 -device virtio-net-pci,netdev=n0,mac=52:54:00:12:34:56,romfile= \
    -serial file:"$work/console.log" -pidfile "$work/qemu.pid" \
    </dev/null >"$work/qemu.log" 2>&1 &
  echo $! >"$work/qemu.launcher"
  date +%s.%N >"$work/boot.t0"
  echo "booting $(sed -n 's/^serial=//p' "$bundle/provenance") (baked ${age}d ago) in the background"
}

build() {
  # the Go toolchain comes from ci/fetch-go.sh, started in the background
  # by the workflow's boot step
  local go=${GOROOT_FETCHED:-${RUNNER_TEMP:-/tmp}/go}
  if [ -n "${RUNNER_TEMP:-}" ]; then
    until [ -e "$go/.ok" ]; do
      [ -e "$go/.failed" ] && { cat "$go.log" 2>/dev/null; echo "::error::fetching Go failed"; exit 1; }
      sleep 0.1
    done
    export PATH=$go/bin:$PATH
  fi
  # static, so the guest needs no Go; one invocation builds both packages
  # in parallel
  CGO_ENABLED=0 go test -c -o "$share/bin/" ./internal/endpoint ./internal/csi
  echo full >"$share/mode"
  touch "$share/ready"
}

await() {
  local pid
  for _ in $(seq 1 50); do [ -s "$work/qemu.pid" ] && break; sleep 0.1; done
  pid=$(cat "$work/qemu.pid" 2>/dev/null || cat "$work/qemu.launcher")
  local deadline=$((SECONDS + 480))
  until [ -e "$share/out/rc" ]; do
    kill -0 "$pid" 2>/dev/null || { echo "::error::the VM exited before the guest wrote a result"; diag; exit 1; }
    [ "$SECONDS" -lt "$deadline" ] || { echo "::error::the guest gave no result in 8 minutes"; diag; exit 1; }
    sleep 0.1
  done
  kill "$pid" 2>/dev/null || true
}

assert_version() {
  local want=$1 ver bundle
  bundle=$(cat "$work/bundle")
  ver=$(cat "$share/out/version.txt" 2>/dev/null || true)
  echo "$ver"
  local rc=0
  # userland and kmod both <want>.x, and the same build
  grep -Eq "^zfs-${want//./\\.}\." <<<"$ver" && grep -Eq "^zfs-kmod-${want//./\\.}\." <<<"$ver" ||
    { echo "::error::expected OpenZFS $want.x userland and kmod, got: $(tr '\n' ' ' <<<"$ver")"; rc=1; }
  local user kmod sysfs
  user=$(sed -n 's/^zfs-\([0-9]\)/\1/p' <<<"$ver")
  kmod=$(sed -n 's/^zfs-kmod-//p' <<<"$ver")
  sysfs=$(sed -n 's/^sysfs: //p' <<<"$ver")
  [ -n "$kmod" ] && [ "$user" = "$kmod" ] || { echo "::error::userland ($user) and kmod ($kmod) versions differ"; rc=1; }
  # the loaded module itself, not only what the tools report
  [ -n "$sysfs" ] && [ "$sysfs" = "$kmod" ] || { echo "::error::/sys/module/zfs/version ($sysfs) is not the kmod zfs reports ($kmod)"; rc=1; }
  # the kernel the bake recorded is the one that booted (direct kernel boot
  # from the bundle; its modules are on the bundle's disk)
  local kernel
  kernel=$(sed -n 's/^kernel=//p' "$bundle/provenance")
  grep -qxF "uname: $kernel" <<<"$ver" || { echo "::error::booted kernel is not the baked one ($kernel)"; rc=1; }
  grep -q '^exportfs: /' <<<"$ver" || { echo "::error::exportfs missing in the VM (nfs-kernel-server not installed?)"; rc=1; }
  local n
  n=$(sed -n 's/^nfsd: //p' <<<"$ver")
  [ "${n:-0}" -gt 0 ] || { echo "::error::nfs-server is not running in the VM"; rc=1; }
  {
    echo "### real ZFS VM: $(prov series)/$(prov build) (prebaked)"
    echo "- image serial: \`$(prov serial)\`, sha256 \`$(prov image_sha256)\` (gpg-verified SHA256SUMS at bake)"
    echo "- baked: $(date -u -d "@$(prov baked_at)" +%F) (inputs \`$(prov bake_inputs)\`)"
    echo "- kernel: \`$kernel\`"
    echo "- zfs version: \`$(grep '^zfs' <<<"$ver" | tr '\n' ' ')\` sysfs \`$sysfs\`"
  } >>"$summary"
  return $rc
}

wait_() {
  local want=${1:-2.4}
  trap 'rc=$?; [ $rc -eq 0 ] || diag; kill "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null || true; exit $rc' EXIT
  await
  local t0
  t0=$(cat "$work/boot.t0")
  echo "guest finished $(since "$t0")s after boot start"
  grep -h 'zfsci:' "$work/console.log" || true
  cat "$share/out/shards.txt" 2>/dev/null || true
  local rc=0
  assert_version "$want" || rc=1
  [ "$(cat "$share/out/rc")" = 0 ] || { echo "::error::the guest run failed (rc $(cat "$share/out/rc"))"; tail -n 40 "$share/out/guest.log"; rc=1; }

  local o=$share/out
  cat "$o"/endpoint.[0-9]*.log >"$work/endpoint.log" 2>/dev/null || rc=1
  cat "$o/csi-other.log" "$o/csi-mount.log" >"$work/csi-mount.log" 2>/dev/null || rc=1
  cp "$o/csi-block.log" "$o/endpoint.list" "$o/csi.list" "$work/" 2>/dev/null || rc=1
  {
    echo "| leg | package | skips | skipped tests |"
    echo "|---|---|---|---|"
  } >>"$summary"
  # the endpoint shards' logs together must cover endpoint.list; csi-mount
  # (the non-sanity tests plus sanity on filesystems) must cover csi.list;
  # csi-sanity spec floor: Identity + Controller specs run on the simulator
  # (44 of 103 with csi-test v5.6); a regex or skip regression drops below it
  ci/check-skips.sh vm endpoint "$work/endpoint.log" "$work/endpoint.list" || rc=1
  ci/check-skips.sh vm csi "$work/csi-mount.log" "$work/csi.list" 44 || rc=1
  ci/check-skips.sh vm csi "$work/csi-block.log" "" 44 || rc=1
  exit $rc
}

smoke() {
  local want=${1:-2.4} bundle=${2:?bundle}
  boot "$bundle"
  echo smoke >"$share/mode"
  touch "$share/ready"
  trap 'rc=$?; [ $rc -eq 0 ] || diag; kill "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null || true; exit $rc' EXIT
  await
  echo "smoke boot to result: $(since "$(cat "$work/boot.t0")")s"
  grep -h 'zfsci:' "$work/console.log" || true
  assert_version "$want"
  [ "$(cat "$share/out/rc")" = 0 ] || { echo "::error::smoke pool create/destroy failed"; cat "$share/out/guest.log"; exit 1; }
}

case $cmd in
  boot) boot "${2:-}" ;;
  build) build ;;
  wait) wait_ "${2:-2.4}" ;;
  smoke) smoke "${2:-2.4}" "${3:-}" ;;
  *) echo "unknown command $cmd" >&2; exit 2 ;;
esac
