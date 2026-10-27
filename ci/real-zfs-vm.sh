#!/usr/bin/env bash
# Run the endpoint contract tests against real ZFS inside an Ubuntu cloud
# image booted under KVM. GitHub has no runner for a non-LTS Ubuntu, and ZFS
# lives in the kernel, so a container would test the host's ZFS instead.
#
# usage: ci/real-zfs-vm.sh <ubuntu-series> [image-build] [openzfs-minor]
#   ci/real-zfs-vm.sh stonking current 2.4  # Ubuntu 26.10 RC track, OpenZFS 2.4.x
# Images come from the per-series tree (cloud-images.ubuntu.com/<series>/),
# whose files are named <series>-server-cloudimg-amd64.img; the releases/
# tree names them ubuntu-<version>-... instead. The image is verified
# against SHA256SUMS, itself verified with the Ubuntu cloud image key
# (/usr/share/keyrings/ubuntu-cloudimage-keyring.gpg, from ubuntu-keyring).
# Both `zfs version` lines (userland and kmod) must be <openzfs-minor>.x.
#
# ZFSVM_CACHE=<dir> keeps the provisioned guest (packages installed) between
# runs: guest apt took 47-73s per run, varying with the mirror. A cold run
# provisions, powers off and stores the disk; a warm run boots a copy-on-
# write overlay of it and skips the image download and apt. The workflow
# keys the directory by the image's sha256 and the hash of
# ci/real-zfs-cloud-config.yaml (what provisioning installs). Every
# boot still runs the version, module and nfsd checks below, so a stale
# disk fails them rather than testing another ZFS. A cold run also stores
# the guest's kernel, initrd and cmdline, and warm boots load them directly
# (-kernel): no SeaBIOS, no GRUB, no BIOS-speed read of the kernel and
# initrd (qemu start to the guest kernel's clock took 3.5-5.8s in runs
# 40468413047 and 40469622947). A disk stored without them boots as before.
#
# ZFSVM_PHASE splits a run so the guest boots while the job sets up Go:
#   boot  verify the image, start the guest, return (qemu keeps running)
#   run   build the test binaries, wait for the guest, check it, run tests
#   all   both, in one process (the default)
# ZFSVM_SUMS=<dir> holds SHA256SUMS and SHA256SUMS.gpg already fetched by
# .github/actions/real-zfs-keys; they are verified again here.
# ZFSVM_SUITE=<suite> runs one suite of ci/real-zfs-suites.txt (endpoint or
# csi): only its test binaries are built, run and checked. ci.yml and
# nightly.yml run one job per suite, each with its own VM; the default,
# all, runs every suite in one guest.
# ZFSVM_PROVISION_ONLY=1 stops after the checks: no build, no tests
# (cache-warm.yml stores a guest with it).
# ZFSDIFF_FULL=1 and ZFS_CRASH_FULL=1 (nightly.yml) reach the guest's tests.
set -euo pipefail

series=${1:?series, e.g. stonking}
build=${2:-current}
want=${3:-2.4}
work=${RUNNER_TEMP:-/tmp}/zfsvm
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
keyring=/usr/share/keyrings/ubuntu-cloudimage-keyring.gpg
cache=${ZFSVM_CACHE:-}
phase=${ZFSVM_PHASE:-all}
case $phase in boot | run | all) ;; *) echo "ZFSVM_PHASE must be boot, run or all" >&2; exit 2 ;; esac
suite=${ZFSVM_SUITE:-all}
mkdir -p "$work"
cd "$(git rev-parse --show-toplevel)"
# only packages whose tests reach a real pool through ZFS_TEST_POOL
# (ci/check-suites.sh); internal/server and internal/zfs run on the
# simulator in the unit job
mapfile -t pkgs < <(awk -v s="$suite" '!/^#/ && NF && (s == "all" || $1 == s) { for (i = 2; i <= NF; i++) print $i }' ci/real-zfs-suites.txt)
[ ${#pkgs[@]} -gt 0 ] || { echo "ZFSVM_SUITE=$suite is not a suite in ci/real-zfs-suites.txt" >&2; exit 2; }
# the unit check (ci/real-zfs-unit.sh) runs once per push: in the csi job,
# whose guest has the CPU to spare, not on the endpoint job's critical path
unit=
case $suite in csi | all) unit=1 ;; esac
has() { case " ${pkgs[*]} " in *" $1 "*) return 0 ;; esac; return 1; }
base="https://cloud-images.ubuntu.com/$series/$build"
img="$work/$series.img"

now_ms() { echo $(($(date +%s%N) / 1000000)); }
# the boot timeline in the log: milliseconds since qemu started
mark() { echo "timeline: $* at +$(($(now_ms) - launched))ms"; }

# one TCP+SSH handshake for the whole run, not one per command
common=(-i "$work/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR
  -o ControlMaster=auto -o ControlPath="$work/ssh-%C" -o ControlPersist=300)
vm() { ssh "${common[@]}" -p 2222 ci@127.0.0.1 "$@"; }

# on failure, show what the guest printed (console: boot errors) and, if it
# answers, cloud-init's own account (apt failures land there); the workflow
# uploads console.log as an artifact
diag() {
  echo "--- guest console (tail) ---"
  tail -n 80 "$work/console.log" 2>/dev/null || true
  echo "--- cloud-init (best effort) ---"
  timeout 60 ssh "${common[@]}" -p 2222 ci@127.0.0.1 \
    'cloud-init status --long; sudo tail -n 80 /var/log/cloud-init-output.log; sudo dmesg | grep -iE "zfs|spl|nfsd" | tail -n 20' || true
}
# keep=1: a successful boot phase leaves the guest running for the run phase
keep=
cleanup() {
  local rc=$?
  [ $rc -eq 0 ] || diag
  ssh "${common[@]}" -O exit -p 2222 ci@127.0.0.1 2>/dev/null || true
  [ -n "$keep" ] || kill "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null || true
  exit $rc
}

# -display none, not -nographic: QEMU refuses -nographic with -daemonize.
# A warm boot reuses the cold boot's seed, though the provisioned disk has
# cloud-init disabled. All the runner's vCPUs and 6G: the tests run as
# concurrent shards (three per suite) with their pools on a guest tmpfs.
# boot <disk> [<dir with vmlinuz, initrd, cmdline>]
boot() {
  local direct=()
  [ -z "${2:-}" ] || direct=(-kernel "$2/vmlinuz" -initrd "$2/initrd" -append "$(cat "$2/cmdline")")
  qemu-system-x86_64 -enable-kvm -cpu host -m 6144 -smp "$(nproc)" -display none "${direct[@]}" \
    -drive file="$1",if=virtio,discard=unmap,cache=unsafe -drive file="$work/seed.img",if=virtio,format=raw \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
    -serial file:"$work/console.log" -daemonize -pidfile "$work/qemu.pid"
  launched=$(now_ms)
  echo "launched=$launched" >>"$work/state"
}
# the stored disk, through a throwaway overlay; its own kernel when stored
overlay() {
  qemu-img create -q -f qcow2 -F qcow2 -b "$cache/disk.qcow2" "$work/run.qcow2" 20G
  if [ -s "$cache/vmlinuz" ] && [ -s "$cache/initrd" ] && [ -s "$cache/cmdline" ] && [ -s "$cache/kernel.release" ]; then
    kernel=$(cat "$cache/kernel.release")
    echo "kernel=$kernel" >>"$work/state"
    echo "direct kernel boot: $kernel, cmdline: $(cat "$cache/cmdline"), initrd $(du -k "$cache/initrd" | cut -f1) KiB"
    boot "$work/run.qcow2" "$cache"
  else
    kernel=
    boot "$work/run.qcow2"
  fi
}

if [ "$phase" != run ]; then
  : >"$work/state"
  # image: verify the signed checksum list, then the image against it
  if [ -n "${ZFSVM_SUMS:-}" ] && [ -s "$ZFSVM_SUMS/SHA256SUMS" ] && [ -s "$ZFSVM_SUMS/SHA256SUMS.gpg" ]; then
    cp "$ZFSVM_SUMS/SHA256SUMS" "$ZFSVM_SUMS/SHA256SUMS.gpg" "$work/"
  else
    curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$base/SHA256SUMS"
    curl -fsSL --retry 3 -o "$work/SHA256SUMS.gpg" "$base/SHA256SUMS.gpg"
  fi
  gpgv --keyring "$keyring" "$work/SHA256SUMS.gpg" "$work/SHA256SUMS"
  file="$series-server-cloudimg-amd64.img"
  sum=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$work/SHA256SUMS")
  [ -n "$sum" ] || { echo "$file is not in $base/SHA256SUMS" >&2; exit 1; }
  warm=
  if [ -n "$cache" ] && [ -f "$cache/ok" ] && [ "$(cat "$cache/sum")" = "$sum" ]; then
    warm=1
    echo "warm guest: $cache (provisioned from image $sum)"
    cp "$cache/key" "$cache/key.pub" "$cache/seed.img" "$work/"
  else
    curl -fsSL --retry 3 -o "$img" "$base/$file"
    echo "$sum  $img" | sha256sum -c -
    qemu-img resize "$img" 20G
    ssh-keygen -q -t ed25519 -N '' -f "$work/key" <<<y >/dev/null
  fi
  sed "s|@SSH_KEY@|$(cat "$work/key.pub")|" ci/real-zfs-cloud-config.yaml >"$work/user-data"
  [ -n "$warm" ] || cloud-localds "$work/seed.img" "$work/user-data"
  printf 'warm=%s\nsum=%s\n' "$warm" "$sum" >>"$work/state"
  trap cleanup EXIT
  # boot first: provisioning (or a warm boot) overlaps setup-go and the
  # test compile
  kernel=
  if [ -n "$warm" ]; then overlay; else boot "$img"; fi
  if [ "$phase" = boot ]; then
    keep=1
    echo "guest started (qemu pid $(cat "$work/qemu.pid")); ZFSVM_PHASE=run continues"
    exit 0
  fi
else
  [ -s "$work/state" ] || { echo "ZFSVM_PHASE=run without a boot phase ($work/state)" >&2; exit 1; }
  warm= sum= launched= kernel=
  # shellcheck source=/dev/null
  . "$work/state"
  trap cleanup EXIT
  kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null || { echo "the guest started by the boot phase is not running" >&2; exit 1; }
fi

if [ -z "${ZFSVM_PROVISION_ONLY:-}" ]; then
  # test binaries: static, so the VM needs no Go toolchain; built as the
  # runner user, only the binaries run as root. One go invocation builds
  # and links both in parallel (-o <dir>/ writes <pkg>.test)
  # beside them, the daemon and the probe for the unit check
  # (ci/real-zfs-unit.sh): 2.5s cold, 0.4s from a warm cache, measured
  # locally; in parallel, so it adds what the test link does not cover
  unitbuild=
  if [ -n "$unit" ]; then
    CGO_ENABLED=0 go build -o "$work/" ./cmd/zfs-endpoint ./ci/unitprobe &
    unitbuild=$!
  fi
  CGO_ENABLED=0 go test -c -o "$work/" "${pkgs[@]/#/./internal/}"
  [ -z "$unitbuild" ] || wait "$unitbuild"
  mark "test binaries built"
fi

wait_ssh() {
  local up=
  # sshd refuses at once until it is up, so a short poll costs nothing
  for _ in $(seq 1 900); do vm true 2>/dev/null && { up=1; break; }; sleep 0.2; done
  [ -n "$up" ] || { echo "VM never became reachable over ssh" >&2; exit 1; }
  mark "ssh up"
}
wait_ssh
# status exits non-zero on recoverable warnings; the version, module and
# nfsd checks below are the real gates
# wait for cloud-init by its marker file: `status --wait` dies with ssh 255
# when sshd restarts during the package install
done_=
for _ in $(seq 1 600); do vm 'test -e /var/lib/cloud/instance/boot-finished' 2>/dev/null && { done_=1; break; }; sleep 1; done
[ -n "$done_" ] || { echo "cloud-init never finished" >&2; exit 1; }
if [ -z "$warm" ] && [ -n "$cache" ]; then
  # store the provisioned disk, then carry on from an overlay of it, as a
  # warm run would; failing to store only costs the next run its speed
  t0=$SECONDS
  # the running kernel, its initrd and command line, for direct kernel
  # boots of the stored disk; best effort, a warm boot without them goes
  # through the BIOS and GRUB
  rel=$(vm 'uname -r')
  if vm "sudo cat /boot/vmlinuz-$rel" >"$work/vmlinuz" && vm "sudo cat /boot/initrd.img-$rel" >"$work/initrd" &&
    vm 'sed -E "s/(^| )BOOT_IMAGE=[^ ]*//; s/^ +//" /proc/cmdline' >"$work/cmdline" &&
    [ -s "$work/vmlinuz" ] && [ -s "$work/initrd" ] && [ -s "$work/cmdline" ]; then
    echo "$rel" >"$work/kernel.release"
  else
    echo "::warning::could not copy the guest's kernel $rel; warm boots will go through the BIOS"
    rm -f "$work/vmlinuz" "$work/initrd" "$work/cmdline" "$work/kernel.release"
  fi
  # trimmed (discard=unmap) and zstd-compressed: the cached disk shrinks
  # from 2.3G, which every warm run restores and writes out
  vm 'sudo apt-get clean; sudo fstrim -av; sudo sync'
  vm 'sudo systemctl poweroff' 2>/dev/null || true
  ssh "${common[@]}" -O exit -p 2222 ci@127.0.0.1 2>/dev/null || true
  for _ in $(seq 1 300); do kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null || break; sleep 0.2; done
  if ! kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null; then
    rm -rf "$cache" && mkdir -p "$cache"
    qemu-img convert -m 16 -W -c -O qcow2 -o compression_type=zstd "$img" "$cache/disk.qcow2"
    cp "$work/key" "$work/key.pub" "$work/seed.img" "$cache/"
    for f in vmlinuz initrd cmdline kernel.release; do [ ! -e "$work/$f" ] || cp "$work/$f" "$cache/"; done
    echo "$sum" >"$cache/sum"
    # cacheable only once a boot from it passes the checks below
    stored=1
    echo "stored provisioned guest in $cache ($(du -sh "$cache" | cut -f1), $((SECONDS - t0))s)"
    overlay
  else
    echo "guest did not power off; not caching it" >&2
    kill "$(cat "$work/qemu.pid")" 2>/dev/null || true
    sleep 2
    boot "$img"
  fi
  wait_ssh
fi
# services (zfs module, nfsd) come up after sshd on a reboot; wait for the
# two the tests need, not for the whole boot to settle
vm 'for _ in $(seq 1 300); do systemctl is-active -q nfs-server && break; sleep 0.1; done'
mark "nfs-server active"
# cloud-init's runcmd ran only on the provisioning boot
vm 'sudo modprobe zfs'
vm 'cloud-init status --long || true; cat /etc/cloud/build.info; uname -r'

ver=$(vm 'zfs version 2>&1' || true) # a missing module fails below, with the output
echo "$ver"
serial=$(vm 'sed -n "s/^serial: *//p" /etc/cloud/build.info')
running=$(vm 'uname -r')
{
  echo "### real ZFS VM: $series/$build, suite $suite (${pkgs[*]})"
  echo "- image serial: \`$serial\`, sha256 \`$sum\` (gpg-verified SHA256SUMS)"
  echo "- kernel: \`$running\`${kernel:+ (direct kernel boot)}"
  echo "- zfs version: \`$(echo "$ver" | tr '\n' ' ')\`"
} >>"$summary"
grep -Eq "^zfs-${want//./\\.}\." <<<"$ver" && grep -Eq "^zfs-kmod-${want//./\\.}\." <<<"$ver" \
  || { echo "expected OpenZFS $want.x userland and kmod, got:" >&2; echo "$ver" >&2; exit 1; }
[ "$(sed -n 's/^zfs-kmod-//p' <<<"$ver")" = "$(sed -n 's/^zfs-\([0-9]\)/\1/p' <<<"$ver")" ] \
  || { echo "userland and kmod versions differ:" >&2; echo "$ver" >&2; exit 1; }
# a direct boot runs the kernel stored with the disk; its modules (zfs) are
# the disk's /lib/modules/<release>, so the two must be the same release
[ -z "$kernel" ] || [ "$running" = "$kernel" ] \
  || { echo "booted kernel $running, the stored disk's kernel is $kernel" >&2; exit 1; }
# what the file-export tests need besides running nfsd threads. Only
# exportfs: libzfs creates /etc/exports.d on the first share, and
# exportfs writes /var/lib/nfs/etab when it first exports
vm 'sudo sh -c "command -v exportfs"' >/dev/null \
  || { echo "exportfs missing in the VM (nfs-kernel-server not installed?)" >&2; exit 1; }
vm 'ls -ld /etc/exports.d /var/lib/nfs/etab 2>&1 || true' # context for an export test failure
vm 'n=$(sudo cat /proc/fs/nfsd/threads 2>/dev/null || echo 0); echo "nfsd threads: $n"; [ "$n" -gt 0 ]' \
  || { echo "nfs-server is not running in the VM" >&2; exit 1; }
if [ -n "${stored:-}" ]; then
  touch "$cache/ok"
  echo "the stored guest booted and passed the version, module and nfsd checks: cacheable"
fi
if [ -n "${ZFSVM_PROVISION_ONLY:-}" ]; then
  echo "ZFSVM_PROVISION_ONLY: guest checked, no tests run"
  exit 0
fi

# where the guest's boot went (kernel, initrd, userspace), for the boot
# timeline. In the background over the ssh master, so it runs beside the
# tests instead of after them (1.7s on the path in run 40494213342), and
# printed after the logs. It waits for the boot to settle first: it fails
# while units are still starting. Best effort, never a gate
# critical-chain: what ssh and nfsd, the two the job waits for, waited for;
# findmnt: /boot and /boot/efi stay unmounted (noauto, cloud-config)
vm 'timeout 60 systemctl is-system-running --wait >/dev/null 2>&1; systemd-analyze; systemd-analyze blame | awk "NR <= 15"
  systemd-analyze critical-chain ssh.service nfs-server.service 2>&1 | awk "NR <= 40"
  echo "mounted under /boot: $(findmnt -rn -o TARGET | awk "/^\/boot/" | tr "\n" " ")"' \
  >"$work/systemd-analyze.txt" 2>&1 &
analyze=$!
bins=()
for pkg in "${pkgs[@]}"; do bins+=("$work/$pkg.test"); done
[ -z "$unit" ] || bins+=("$work/zfs-endpoint" "$work/unitprobe" ci/real-zfs-unit.sh deploy/systemd/zfs-endpoint@.service \
  deploy/systemd/zfs-endpoint@.service.d/hardening.conf)
scp "${common[@]}" -P 2222 "${bins[@]}" ci/real-zfs-guest.sh ci/real-zfs-weights.txt ci/test-times.awk ci@127.0.0.1:
rc=0
# concurrent shards on tmpfs-backed pools, three per suite (ci/real-zfs-guest.sh);
# the logs come back per shard and are merged per check below.
# ZFSDIFF_FULL=1 replays the whole zfsdiff corpus, ZFS_CRASH_FULL=1 runs
# every crash point on the real pool (both nightly.yml)
vm "sudo env ZFSVM_PKGS='${pkgs[*]}' ZFSVM_UNIT=$unit ZFSDIFF_FULL=${ZFSDIFF_FULL:-} ZFS_CRASH_FULL=${ZFS_CRASH_FULL:-} bash real-zfs-guest.sh" || rc=$?
mkdir -p "$work/out"
scp "${common[@]}" -P 2222 'ci@127.0.0.1:out/*' "$work/out/" || rc=1
o=$work/out
for f in "$o"/*.log; do
  echo "::group::$(basename "$f")"
  cat "$f"
  echo "::endgroup::"
done
# the guest's CPU and pressure over the test phase (ci/real-zfs-guest.sh):
# what decides whether splitting the job across two runners would pay
if [ -s "$o/load.txt" ]; then
  echo "::group::guest load (test phase): $(awk 'NR == 2' "$o/load.txt")"
  cat "$o/load.txt"
  cat "$o/vmstat.txt" 2>/dev/null || true
  echo "::endgroup::"
  { echo '<details><summary>guest load over the test phase (out/load.txt)</summary>'
    echo; echo '```'; cat "$o/load.txt"; echo '```'; echo "</details>"; echo; } >>"$summary"
fi
echo "::group::systemd-analyze"
wait "$analyze" || true
cat "$work/systemd-analyze.txt" 2>/dev/null || true
echo "::endgroup::"
# what check-skips reads: endpoint = the union of its shards (it must
# cover endpoint.list); csi-mount = the non-sanity tests plus csi-sanity on
# filesystems (it must cover csi.list); csi-block = csi-sanity on zvols
if has endpoint; then
  cat "$o"/endpoint.[0-9].log >"$work/endpoint.log" || rc=1
  cp "$o/endpoint.list" "$work/" || rc=1
fi
if has csi; then
  cat "$o/csi-other.log" "$o/csi-mount.log" >"$work/csi-mount.log" || rc=1
  cp "$o/csi-block.log" "$o/csi.list" "$work/" || rc=1
fi
# the endpoint suite's reports. TestNFSReplay (internal/endpoint) replays a sharenfs corpus through the
# real libshare, exportfs and etab and diffs it against the reference
# models (internal/nfsref) and the simulator; the guest points its report
# at out/nfs-replay.log. Its last line is the tally.
if ! has endpoint; then
  :
elif [ -f "$o/nfs-replay.log" ]; then
  { echo "<details><summary>NFS replay: $(tail -n 1 "$o/nfs-replay.log")</summary>"
    echo; echo '```'; cat "$o/nfs-replay.log"; echo '```'; echo "</details>"; echo; } >>"$summary"
else
  echo "::error::vm/endpoint: TestNFSReplay wrote no nfs-replay.log"; rc=1
fi
# TestZFSDiffReplay replays zfs(8) sequences (internal/zfs/zfsdiff) on the
# pool, the simulator and the reference model. Divergences are reported,
# not failed (ZFSDIFF_STRICT=1 would); the summary gets the verdict per
# sequence, the workflow uploads the whole report. Its last line is the
# tally with the replay's wall time.
if ! has endpoint; then
  :
elif [ -f "$o/zfsdiff.log" ]; then
  { echo "<details><summary>zfsdiff replay: $(tail -n 1 "$o/zfsdiff.log")</summary>"
    echo; echo '```'; sed -n '3,/^$/p' "$o/zfsdiff.log"; echo '```'; echo "</details>"; echo; } >>"$summary"
else
  echo "::error::vm/endpoint: TestZFSDiffReplay wrote no zfsdiff.log"; rc=1
fi
# the unit and its hardening drop-in under systemd (ci/real-zfs-unit.sh):
# its last line is the verdict, naming any drop-in setting that broke it
if [ -z "$unit" ]; then
  :
elif [ -s "$o/unit-hardening.log" ]; then
  verdict=$(tail -n 1 "$o/unit-hardening.log")
  echo "- $verdict" >>"$summary"
  case $verdict in "unit hardening: PASS"*) ;; *) echo "::error::vm: $verdict"; rc=1 ;; esac
else
  echo "::error::vm: the unit check wrote no unit-hardening.log"; rc=1
fi
# per-test time (ci/test-times.awk): the data ci/real-zfs-weights.sh turns
# into ci/real-zfs-weights.txt; the workflow uploads test-times.tsv
if [ -s "$o/test-times.tsv" ]; then
  {
    echo "<details><summary>real-ZFS shard walls and slowest tests (out/test-times.tsv)</summary>"
    echo
    echo '```'
    cat "$o/shards.txt" 2>/dev/null || true
    echo '```'
    echo
    echo "| shard | test | status | parallel | own s | with parallel subtests s | wall s | subtests |"
    echo "|---|---|---|---|---|---|---|---|"
    # awk reads all of sort's output: head would close the pipe early, and
    # under pipefail sort's SIGPIPE failed the job (run 40496789015)
    grep -v '^#' "$o/test-times.tsv" | sort -t "$(printf '\t')" -k6,6gr |
      awk -F '\t' 'NR <= 25 { printf "| %s | %s | %s | %s | %s | %s | %s | %s |\n", $1, $2, $3, $4, $5, $6, $7, $8 }'
    echo
    echo "</details>"
    echo
  } >>"$summary"
else
  echo "::warning::vm: the guest wrote no test-times.tsv"
fi
{
  echo "| leg | package | skips | skipped tests |"
  echo "|---|---|---|---|"
} >>"$summary"
# csi-sanity spec floor: Identity + Controller specs run on the simulator
# (44 of 103 with csi-test v5.6); a regex or skip regression drops below it
# each suite's checks, in the job that runs it: the jobs together cover
# every real-pool package once (ci/check-suites.sh, in the static job)
if has endpoint; then
  ci/check-skips.sh vm endpoint "$work/endpoint.log" "$work/endpoint.list" || rc=1
  # every endpoint test in exactly one shard; TestPropertyRoundTrip in
  # every shard, a part of its rows each (PROPRT_SHARD), every row in
  # exactly one of them
  ci/check-split.sh TestPropertyRoundTrip "$work/endpoint.list" "$o"/endpoint.[0-9].log || rc=1
fi
if has csi; then
  ci/check-skips.sh vm csi "$work/csi-mount.log" "$work/csi.list" 44 || rc=1
  ci/check-skips.sh vm csi "$work/csi-block.log" "" 44 || rc=1
fi
exit $rc
