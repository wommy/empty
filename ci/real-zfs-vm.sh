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
# the guest's kernel and cmdline, and warm boots load them directly
# (-kernel): no SeaBIOS, no GRUB, no BIOS-speed read of the kernel and
# initrd (qemu start to the guest kernel's clock took 3.5-5.8s in runs
# 40468413047 and 40469622947). A disk stored without them boots as before.
# A direct boot of the cloud image's root (root=LABEL=cloudimg-rootfs, GPT
# partition 1) skips the initrd too: the kernel has virtio-pci, virtio-blk
# and ext4 built in (=y in config-7.3.0-9-generic), so it mounts /dev/vda1
# itself, and the 4.6s initrd stage (run 40505456077) goes. If that kernel
# cannot (it panics, qemu exits: -no-reboot), the guest boots again through
# the BIOS and GRUB, from the disk's own /boot (kernel and initrd), and the
# job warns. ZFSVM_INITRD=1 always boots that way. The initrd is not stored
# beside the disk (since cache key v5): it is already on the disk, and the
# 66 MB copy was in every warm restore for a fallback that reads the disk.
# Every direct boot masks zfs-import-cache and zfs-import-scan from the
# kernel command line: they Require systemd-udev-settle, which held
# local-fs.target, so sshd, until every device settled (zfs-import.target
# at 3.4s of the 4.1s to ssh in run 40505456077); no pool exists at boot,
# the tests create theirs. No cache key change: the stored cmdline is
# rewritten at boot.
# systemd-udev-settle itself is masked too: with the imports masked,
# zfsutils-linux's zfs-load-module.service (2.4.4-1ubuntu4: Wants= and
# After=systemd-udev-settle.service) still pulled it in and ordered
# zfs-mount, so local-fs.target and ssh, behind it (1.78s, run
# 40511369632). A Wants= on a masked unit is no failure, and no other
# unit of the guest's packages names it (nfs-kernel-server, nfs-common,
# multipath-tools, udisks2, lvm2, mdadm, open-iscsi). zvol links come
# from udevd's rules (60-zvol.rules), which still run; the endpoint waits
# for them with `udevadm settle` (UdevSettle), a call to udevd, not this
# unit. The boot diagnostics below print who still wants it and any
# failed unit.
# systemd-modules-load, zfs-load-module and zfs-mount are masked as well
# (ADR 0005, "ssh poll and module load"): the first loaded zfs (from
# /etc/modules-load.d/zfs.conf) on sysinit.target's chain, so ssh's
# (0.98s, 2.6s in the csi job, run 40513522394), and the other two held
# local-fs.target, so ssh and nfs-server (1.05s in csi). No pool exists at
# boot, so zfs-mount had nothing to mount. The checks below load zfs
# explicitly (`modprobe zfs`, timed) before anything uses it and start
# zed, which skipped its boot start (ConditionPathIsDirectory=
# /sys/module/zfs). nfsd is not in modules-load.d: mounting
# /proc/fs/nfsd loads it, as before. A GRUB boot (the cold run, the panic
# fallback) masks nothing.
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
# ZFSVM_PART=<p>/<P> (default 0/1) runs part p of the endpoint suite split
# over P jobs: the host lists the endpoint binary and plans P*3 shards
# (ci/real-zfs-plan.sh, a pure function of the list and
# ci/real-zfs-weights.txt), the guest runs shards 3p..3p+2, and
# ci/check-split.sh proves the plan a partition of the list over all P parts
# and this part's logs exactly its shards. ci.yml runs endpoint as 0/2 and
# 1/2 (ADR 0005); only the endpoint suite splits.
# ZFSVM_PROVISION_ONLY=1 stops after the checks: no build, no tests
# (cache-warm.yml stores a guest with it).
# ZFSDIFF_FULL=1 and ZFS_CRASH_FULL=1 (nightly.yml) reach the guest's tests.
# ZFSVM_CSI_NFS=1 (nightly.yml's csi job only, ADR 0005) adds a fourth csi
# shard: csi-sanity on an endpoint in NFS mode, pool zcsi3.
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
# the endpoint suite's part (ci/real-zfs-plan.sh): 3 shards per part
part=${ZFSVM_PART:-0/1}
per=3
[[ $part =~ ^([0-9]+)/([1-9][0-9]*)$ ]] && [ "${BASH_REMATCH[1]}" -lt "${BASH_REMATCH[2]}" ] ||
  { echo "ZFSVM_PART=$part: want <p>/<P> with 0 <= p < P" >&2; exit 2; }
parts=${BASH_REMATCH[2]}
[ "$parts" = 1 ] || [ "$suite" = endpoint ] ||
  { echo "ZFSVM_PART=$part: only the endpoint suite splits, not $suite" >&2; exit 2; }
base="https://cloud-images.ubuntu.com/$series/$build"
img="$work/$series.img"

now_ms() { echo $(($(date +%s%N) / 1000000)); }
# the boot timeline in the log: milliseconds since qemu started
mark() { echo "timeline: $* at +$(($(now_ms) - launched))ms"; }

# one TCP+SSH handshake for the whole run, not one per command. Keepalives
# on the master: slirp keeps the host's TCP connection up whatever the
# guest does, so a hung guest kernel would otherwise hold every command
# over the master (the test run included) until the job's timeout; 60s
# without an answer ends the master and its commands
common=(-i "$work/key" -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=5 -o LogLevel=ERROR
  -o ServerAliveInterval=10 -o ServerAliveCountMax=6
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
  # a build still running when a check failed
  [ -z "${build:-}" ] || { pkill -P "$build" 2>/dev/null; kill "$build" 2>/dev/null; } || true
  [ -n "$keep" ] || kill "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null || true
  exit $rc
}

# -display none, not -nographic: QEMU refuses -nographic with -daemonize.
# A warm boot reuses the cold boot's seed, though the provisioned disk has
# cloud-init disabled. All the runner's vCPUs and 6G: the tests run as
# concurrent shards (three per suite) with their pools on a guest tmpfs.
# boot <disk> [<dir with vmlinuz, initrd, cmdline>]; $noinitrd: no initrd
masks="systemd.mask=zfs-import-cache.service systemd.mask=zfs-import-scan.service systemd.mask=systemd-udev-settle.service"
masks+=" systemd.mask=systemd-modules-load.service systemd.mask=zfs-load-module.service systemd.mask=zfs-mount.service"
# the endpoint shards are fork/exec- and syscall-bound (sys+irq ~57 of ~83
# CPU-s, run 40513522394): no CPU-vulnerability mitigations (syscall and
# context-switch entry/exit work, costlier under nested KVM) and no
# zeroing of every allocated page (Ubuntu's init_on_alloc=1) in this
# throwaway, single-tenant guest. ZFS code paths are unchanged. Direct
# boots only, like the masks; the summary prints the vulnerabilities
# files so the effect is visible
guestcpu="mitigations=off init_on_alloc=0"
boot() {
  local direct=() append
  if [ -n "${2:-}" ]; then
    append="$(cat "$2/cmdline") $masks $guestcpu"
    # no initrd. panic=1 with -no-reboot: a kernel that cannot mount the
    # root exits qemu, and wait_ssh boots again through GRUB. rootwait=10
    # (seconds, kernel >= 6.5), not a bare rootwait: a kernel whose virtio
    # block driver is a module never sees /dev/vda1, and would wait for it
    # forever instead of panicking into the fallback
    append=$(sed -E 's#(^| )root=LABEL=cloudimg-rootfs( |$)#\1root=/dev/vda1 rootfstype=ext4 rootwait=10\2#' <<<"$append")
    direct=(-kernel "$2/vmlinuz" -append "$append panic=1" -no-reboot)
  fi
  qemu-system-x86_64 -enable-kvm -cpu host -m 6144 -smp "$(nproc)" -display none "${direct[@]}" \
    -drive file="$1",if=virtio,discard=unmap,cache=unsafe -drive file="$work/seed.img",if=virtio,format=raw \
    -netdev user,id=n0,hostfwd=tcp:127.0.0.1:2222-:22 -device virtio-net-pci,netdev=n0 \
    -serial file:"$work/console.log" -daemonize -pidfile "$work/qemu.pid"
  launched=$(now_ms)
  echo "launched=$launched" >>"$work/state"
}
# the stored disk, through a throwaway overlay; its own kernel, directly
# and with no initrd, when stored; otherwise through the BIOS and GRUB
overlay() {
  qemu-img create -q -f qcow2 -F qcow2 -b "$cache/disk.qcow2" "$work/run.qcow2" 20G
  kernel= noinitrd=
  if [ -s "$cache/vmlinuz" ] && [ -s "$cache/cmdline" ] && [ -s "$cache/kernel.release" ]; then
    kernel=$(cat "$cache/kernel.release")
    echo "kernel=$kernel" >>"$work/state"
    # the stored cmdline names the cloud image's root by label, which only
    # an initrd resolves; partition 1 of the image carries that label
    if [ -z "${ZFSVM_INITRD:-}" ] && grep -Eq '(^| )root=LABEL=cloudimg-rootfs( |$)' "$cache/cmdline"; then noinitrd=1; fi
  fi
  echo "noinitrd=$noinitrd" >>"$work/state"
  if [ -n "$noinitrd" ]; then
    echo "direct kernel boot: $kernel, cmdline: $(cat "$cache/cmdline"), no initrd (root=/dev/vda1)"
    boot "$work/run.qcow2" "$cache"
  else
    echo "boot through the BIOS and GRUB, with the disk's kernel and initrd${kernel:+ ($kernel)}"
    boot "$work/run.qcow2"
  fi
}

if [ "$phase" != run ]; then
  : >"$work/state"
  # image: verify the signed checksum list, then the image against it
  # build-info.txt: the serial `current` pointed at when the sums were
  # fetched (best effort; a cold run downloads the image from that serial)
  rm -f "${work:?}/build-info.txt"
  if [ -n "${ZFSVM_SUMS:-}" ] && [ -s "$ZFSVM_SUMS/SHA256SUMS" ] && [ -s "$ZFSVM_SUMS/SHA256SUMS.gpg" ]; then
    cp "$ZFSVM_SUMS/SHA256SUMS" "$ZFSVM_SUMS/SHA256SUMS.gpg" "$work/"
    [ ! -s "$ZFSVM_SUMS/build-info.txt" ] || cp "$ZFSVM_SUMS/build-info.txt" "$work/"
  else
    curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$base/SHA256SUMS"
    curl -fsSL --retry 3 -o "$work/SHA256SUMS.gpg" "$base/SHA256SUMS.gpg"
    curl -fsSL --retry 3 -o "$work/build-info.txt" "$base/unpacked/build-info.txt" || true
  fi
  gpgv --keyring "$keyring" "$work/SHA256SUMS.gpg" "$work/SHA256SUMS"
  file="$series-server-cloudimg-amd64.img"
  sum=$(awk -v f="$file" '$2 == f || $2 == "*" f { print $1 }' "$work/SHA256SUMS")
  [ -n "$sum" ] || { echo "$file is not in $base/SHA256SUMS" >&2; exit 1; }
  warm=
  # a cache restore that failed part-way leaves what it extracted: the ok
  # marker with a truncated disk would boot "warm" on a broken disk.
  # qemu-img check reads only the qcow2 metadata and finds a cluster past
  # the end of the file (16ms on a 607 MB zstd image of 20G, 57ms with a
  # cold page cache, measured locally)
  if [ -n "$cache" ] && [ -f "$cache/ok" ] && [ "$(cat "$cache/sum")" = "$sum" ]; then
    if qemu-img check -q "$cache/disk.qcow2"; then
      warm=1
    else
      echo "::warning::the restored guest's disk fails qemu-img check (a partial cache restore?); provisioning a new one"
    fi
  fi
  if [ -n "$warm" ]; then
    echo "warm guest: $cache (provisioned from image $sum)"
    cp "$cache/key" "$cache/key.pub" "$cache/seed.img" "$work/"
  else
    # from the dated serial directory the verified sums came from, not
    # current/, which may move to a new serial between the two fetches;
    # used only if that directory's sums are the verified ones
    src=$base
    imgserial=$(awk -F= '$1 == "serial" { sub(/\r$/, "", $2); print $2 }' "$work/build-info.txt" 2>/dev/null) || imgserial=
    if [ "$build" = current ] && [[ $imgserial =~ ^[0-9]{8}(\.[0-9]+)?$ ]]; then
      if curl -fsSL --retry 3 -o "$work/SHA256SUMS.serial" "https://cloud-images.ubuntu.com/$series/$imgserial/SHA256SUMS" &&
        cmp -s "$work/SHA256SUMS.serial" "$work/SHA256SUMS"; then
        src=https://cloud-images.ubuntu.com/$series/$imgserial
      else
        echo "::warning::$series/$imgserial/SHA256SUMS is not the verified list; downloading the image from $base"
      fi
    fi
    echo "image: $src/$file"
    curl -fsSL --retry 3 -o "$img" "$src/$file"
    echo "$sum  $img" | sha256sum -c -
    qemu-img resize "$img" 20G
    ssh-keygen -q -t ed25519 -N '' -f "$work/key" <<<y >/dev/null
  fi
  sed "s|@SSH_KEY@|$(cat "$work/key.pub")|" ci/real-zfs-cloud-config.yaml >"$work/user-data"
  # the workflows install cloud-image-utils on a cache miss only; a hit
  # whose disk failed the check above provisions all the same
  if [ -z "$warm" ] && ! command -v cloud-localds >/dev/null; then
    echo "::warning::installing cloud-image-utils for a cold guest after a cache hit"
    sudo apt-get update -qq && sudo apt-get install -y -qq --no-install-recommends cloud-image-utils
  fi
  [ -n "$warm" ] || cloud-localds "$work/seed.img" "$work/user-data"
  printf 'warm=%s\nsum=%s\n' "$warm" "$sum" >>"$work/state"
  trap cleanup EXIT
  # boot first: provisioning (or a warm boot) overlaps setup-go and the
  # test compile
  kernel= noinitrd=
  if [ -n "$warm" ]; then overlay; else boot "$img"; fi
  if [ "$phase" = boot ]; then
    keep=1
    echo "guest started (qemu pid $(cat "$work/qemu.pid")); ZFSVM_PHASE=run continues"
    exit 0
  fi
else
  [ -s "$work/state" ] || { echo "ZFSVM_PHASE=run without a boot phase ($work/state)" >&2; exit 1; }
  warm= sum= launched= kernel= noinitrd=
  # shellcheck source=/dev/null
  . "$work/state"
  trap cleanup EXIT
  # a direct boot that panicked (no root without an initrd) exits qemu
  # within seconds, before this phase starts: wait_ssh boots it through GRUB
  kill -0 "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null || [ -n "$noinitrd" ] ||
    { echo "the guest started by the boot phase is not running" >&2; exit 1; }
fi

build=
if [ -z "${ZFSVM_PROVISION_ONLY:-}" ]; then
  # test binaries: static, so the VM needs no Go toolchain; built as the
  # runner user, only the binaries run as root. One go invocation builds
  # and links both in parallel (-o <dir>/ writes <pkg>.test)
  # beside them, the daemon and the probe for the unit check
  # (ci/real-zfs-unit.sh): 2.5s cold, 0.4s from a warm cache, measured
  # locally; in parallel, so it adds what the test link does not cover.
  # In the background: the wait for ssh and the guest checks below run
  # beside the build (they took 0.7s after ssh in run 40505456077), and
  # the scp waits for it
  (
    unitbuild=
    if [ -n "$unit" ]; then
      CGO_ENABLED=0 go build -o "$work/" ./cmd/zfs-endpoint ./ci/unitprobe &
      unitbuild=$!
    fi
    CGO_ENABLED=0 go test -c -o "$work/" "${pkgs[@]/#/./internal/}"
    # the endpoint plan, from the binary's list (static; -test.list runs no
    # test) and the weights: every part of the suite computes the same plan
    # from the same commit (ci/real-zfs-plan.sh), the guest runs this
    # part's shards of it
    if has endpoint; then
      "$work/endpoint.test" -test.list '.*' >"$work/endpoint.list"
      ci/real-zfs-plan.sh "$work/endpoint.list" ci/real-zfs-weights.txt "$parts" "$per" >"$work/endpoint.plan"
    fi
    [ -z "$unitbuild" ] || wait "$unitbuild"
    mark "test binaries built"
  ) &
  build=$!
fi

# Logins wait for the serial getty's login prompt in the console log, and
# that wait is what made ssh prompt: getty starts after
# systemd-user-sessions, which lifts /run/nologin. Before it, sshd answers
# but pam_nologin refuses the login, and OpenSSH (>= 9.8) charges each
# refusal to the source as an authentication failure (PerSourcePenalties
# authfail:5s) and refuses every connection from it once the charge passes
# 15s. All of the host's connections reach the guest from one address
# (slirp), so early logins locked the host out: it saw sshd 16-19s after
# the guest had booted, with 8 penalised drops per boot (runs 40519558807,
# 40535710051). Without a prompt in 60s (a console that prints none) the
# poll goes on without it.
# Each probe first reads the sshd banner on a bare connection (bounded:
# slirp's hostfwd accepts at once and its SYN to a guest without an
# address is retried only after seconds, so a stalled probe is dropped and
# the next sends a fresh SYN); only then does ssh log in (and open the
# master), with the full ConnectTimeout for a guest busy booting.
# A qemu that exits is seen at once, on every pass: a direct boot that
# panics (no root without an initrd) boots again through GRUB; any other
# exit fails the job instead of polling to the deadline.
banner() { (exec 3<>/dev/tcp/127.0.0.1/2222 && IFS= read -r -t "$1" -n 4 b <&3 && [ "$b" = SSH- ]) 2>/dev/null; }
wait_ssh() {
  local up= probes=0 deadline=$((SECONDS + 600)) gate=$((SECONDS + 60)) prompt= qpid
  qpid=$(cat "$work/qemu.pid" 2>/dev/null) || qpid= # qemu removes it when it exits
  # 600s: a cold run's first boot (cloud-init writes the key) included
  while [ $SECONDS -lt $deadline ]; do
    if ! kill -0 "$qpid" 2>/dev/null; then
      if [ -z "$noinitrd" ]; then
        echo "qemu exited before the guest's ssh came up (console.log)" >&2
        exit 1
      fi
      cp "$work/console.log" "$work/console-noinitrd.log" 2>/dev/null || true
      echo "::warning::the guest exited booting without an initrd (console-noinitrd.log); booting it through the BIOS and GRUB, with the disk's initrd"
      noinitrd=
      echo "noinitrd=" >>"$work/state"
      boot "$work/run.qcow2"
      qpid=$(cat "$work/qemu.pid" 2>/dev/null) || qpid=
      prompt= gate=$((SECONDS + 60))
      continue
    fi
    if [ -z "$prompt" ]; then
      # agetty's "<host> login: " anywhere on a line: it ends with no
      # newline, so console output after it (kernel messages, cloud-init's
      # streaming on a cold boot) lands on the same line; \r-safe
      if awk '/[^ ] login:( |\r|$)/ { f = 1 } END { exit !f }' "$work/console.log" 2>/dev/null; then
        prompt=1
        mark "login prompt"
      elif [ $SECONDS -lt $gate ]; then
        sleep 0.1
        continue
      else
        prompt=none
      fi
    fi
    probes=$((probes + 1))
    banner 1 && vm true 2>/dev/null && { up=1; break; }
    sleep 0.1
  done
  [ -n "$up" ] || { echo "VM never became reachable over ssh" >&2; exit 1; }
  mark "ssh up ($probes probes)"
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
  # what the cloud-config's slimming did (a purge it skipped says why), and
  # the space in use: the data behind the stored size
  vm 'sudo awk "/^slim:/" /var/log/cloud-init-output.log; df -h / /boot' || true
  # the running kernel and its command line, for direct kernel boots of
  # the stored disk (no initrd: the fallback boots the disk's own through
  # GRUB); best effort, a warm boot without them goes through the BIOS
  rel=$(vm 'uname -r')
  if vm "sudo cat /boot/vmlinuz-$rel" >"$work/vmlinuz" &&
    vm 'sed -E "s/(^| )BOOT_IMAGE=[^ ]*//; s/^ +//" /proc/cmdline' >"$work/cmdline" &&
    [ -s "$work/vmlinuz" ] && [ -s "$work/cmdline" ]; then
    echo "$rel" >"$work/kernel.release"
  else
    echo "::warning::could not copy the guest's kernel $rel; warm boots will go through the BIOS"
    rm -f "$work/vmlinuz" "$work/cmdline" "$work/kernel.release"
  fi
  # trimmed (discard=unmap) and zstd-compressed: the cached disk shrinks
  # from 2.3G, which every warm run restores and writes out. The
  # provisioning journal goes too (the slimming itself is in the
  # cloud-config, which keys the guest)
  vm 'sudo apt-get clean; sudo journalctl --rotate >/dev/null 2>&1; sudo journalctl --vacuum-size=1 >/dev/null 2>&1; sudo fstrim -av; sudo sync'
  vm 'sudo systemctl poweroff' 2>/dev/null || true
  ssh "${common[@]}" -O exit -p 2222 ci@127.0.0.1 2>/dev/null || true
  for _ in $(seq 1 300); do kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null || break; sleep 0.2; done
  if ! kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null; then
    rm -rf "${cache:?}" && mkdir -p "$cache"
    qemu-img convert -m 16 -W -c -O qcow2 -o compression_type=zstd "$img" "$cache/disk.qcow2"
    cp "$work/key" "$work/key.pub" "$work/seed.img" "$cache/"
    for f in vmlinuz cmdline kernel.release; do [ ! -e "$work/$f" ] || cp "$work/$f" "$cache/"; done
    echo "$sum" >"$cache/sum"
    # cacheable only once a boot from it passes the checks below
    stored=1
    echo "stored provisioned guest in $cache ($(du -sh "$cache" | cut -f1), $((SECONDS - t0))s)"
    overlay
  else
    echo "guest did not power off; not caching it" >&2
    # the new qemu binds the same host port (hostfwd 2222) and console.log:
    # the old one must be gone first, by SIGKILL after 10s
    qpid=$(cat "$work/qemu.pid" 2>/dev/null) || qpid=
    if [ -n "$qpid" ]; then
      kill "$qpid" 2>/dev/null || true
      for _ in $(seq 1 50); do kill -0 "$qpid" 2>/dev/null || break; sleep 0.2; done
      kill -9 "$qpid" 2>/dev/null || true
      for _ in $(seq 1 25); do kill -0 "$qpid" 2>/dev/null || break; sleep 0.2; done
    fi
    boot "$img"
  fi
  wait_ssh
fi
# the zfs module: a direct boot masks its load (systemd-modules-load,
# zfs-load-module), so it is loaded here, timed, before anything uses it;
# on a GRUB boot it is already loaded and modprobe returns at once. zed
# skipped its boot start without the module (ConditionPathIsDirectory=
# /sys/module/zfs) and is started as the boot would have; best effort, the
# diagnostics below print its state. In the background over the master:
# the checks that do not need zfs run beside it
vm 's=$(date +%s%N); sudo modprobe zfs; r=$?; echo "modprobe zfs: $((($(date +%s%N) - s) / 1000000))ms, exit $r"
  [ $r -ne 0 ] || sudo systemctl start --no-block zfs-zed.service || true; exit $r' >"$work/modprobe.txt" 2>&1 &
modprobe=$!
vm 'cloud-init status --long || true; cat /etc/cloud/build.info; uname -r'
serial=$(vm 'sed -n "s/^serial: *//p" /etc/cloud/build.info')
running=$(vm 'uname -r')
# a direct boot runs the kernel stored with the disk; its modules (zfs) are
# the disk's /lib/modules/<release>, so the two must be the same release
[ -z "$kernel" ] || [ "$running" = "$kernel" ] \
  || { echo "booted kernel $running, the stored disk's kernel is $kernel" >&2; exit 1; }
# without an initrd the kernel mounted /dev/vda1 by name: it must be the
# image's root, the partition the stored cmdline named by label
if [ -n "$noinitrd" ]; then
  rootfs=$(vm 'echo "$(findmnt -no SOURCE /) $(sudo blkid -s LABEL -o value /dev/vda1)"')
  [ "$rootfs" = "/dev/vda1 cloudimg-rootfs" ] \
    || { echo "no-initrd boot: expected / on /dev/vda1 labelled cloudimg-rootfs, got: $rootfs" >&2; exit 1; }
fi
# what the file-export tests need besides running nfsd threads. Only
# exportfs: libzfs creates /etc/exports.d on the first share, and
# exportfs writes /var/lib/nfs/etab when it first exports
vm 'sudo sh -c "command -v exportfs"' >/dev/null \
  || { echo "exportfs missing in the VM (nfs-kernel-server not installed?)" >&2; exit 1; }
vm 'ls -ld /etc/exports.d /var/lib/nfs/etab 2>&1 || true' # context for an export test failure
wait "$modprobe" || { cat "$work/modprobe.txt"; echo "modprobe zfs failed in the VM" >&2; exit 1; }
cat "$work/modprobe.txt"
mark "zfs loaded"
ver=$(vm 'zfs version 2>&1' || true) # a missing module fails below, with the output
echo "$ver"
{
  echo "### real ZFS VM: $series/$build, suite $suite (${pkgs[*]})$([ "$parts" = 1 ] || echo ", part $part")"
  echo "- image serial: \`$serial\`, sha256 \`$sum\` (gpg-verified SHA256SUMS)"
  echo "- kernel: \`$running\` ($([ -n "$noinitrd" ] && echo "direct kernel boot, no initrd" || echo "BIOS, GRUB and the disk's initrd"))"
  echo "- zfs version: \`$(echo "$ver" | tr '\n' ' ')\`"
} >>"$summary"
# the guest's mitigations and cmdline ($guestcpu on a direct boot), once
vuln=$(vm 'cat /proc/cmdline; grep . /sys/devices/system/cpu/vulnerabilities/*' || true)
vulnsum="$(awk '/:Vulnerable/ { n++ } END { print n + 0 }' <<<"$vuln") of $(awk '/vulnerabilities\// { n++ } END { print n + 0 }' <<<"$vuln") vulnerable"
echo "guest CPU mitigations: $vulnsum"
{ echo "<details><summary>guest CPU mitigations: $vulnsum</summary>"
  echo; echo '```'; echo "$vuln"; echo '```'; echo "</details>"; echo; } >>"$summary"
grep -Eq "^zfs-${want//./\\.}\." <<<"$ver" && grep -Eq "^zfs-kmod-${want//./\\.}\." <<<"$ver" \
  || { echo "expected OpenZFS $want.x userland and kmod, got:" >&2; echo "$ver" >&2; exit 1; }
# userland and kmod must be the same OpenZFS release; the distribution
# revision may differ (zfs-2.4.4-1ubuntu5 userland from the archive with
# a kernel still built against 1ubuntu4: the archive moves first), and is
# printed, not gated
urev=$(sed -n 's/^zfs-\([0-9]\)/\1/p' <<<"$ver") krev=$(sed -n 's/^zfs-kmod-//p' <<<"$ver")
[ -n "$urev" ] && [ "${urev%%-*}" = "${krev%%-*}" ] \
  || { echo "userland and kmod OpenZFS versions differ (${urev%%-*} and ${krev%%-*}):" >&2; echo "$ver" >&2; exit 1; }
echo "zfs userland $urev, kmod $krev"
[ "$urev" = "$krev" ] || echo "::warning::zfs userland $urev and kmod $krev: the same OpenZFS ${urev%%-*}, different distribution revisions"
# nfs-server comes up after sshd (behind network-online.target, 2.2s
# after ssh in run 40513522394): wait for it last, so the checks above
# run while it starts, not for the whole boot to settle. Up to 120s:
# systemd-networkd-wait-online's own bound on a link slow to come up. The
# loop's status is whether it came up (a bare loop ends with sleep's 0);
# if not, what it is waiting for (critical-chain only answers once the
# boot has finished, so the pending jobs too)
vm 'ok=; for _ in $(seq 1 1200); do if systemctl is-active -q nfs-server; then ok=1; break; fi; sleep 0.1; done; [ -n "$ok" ]' || {
  echo "nfs-server did not become active in 120s" >&2
  vm 'systemctl status --no-pager nfs-server 2>&1 | awk "NR <= 30"; systemctl list-jobs --no-pager 2>&1 | awk "NR <= 30"
    systemd-analyze critical-chain nfs-server.service 2>&1 | awk "NR <= 30"' >&2 || true
  exit 1
}
mark "nfs-server active"
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
# blame without .device units (they filled all 15 lines in run 40505456077);
# udev-settle: masked (inactive), what still wants it, and any failed unit
# (one that Requires= a masked unit fails); systemd-modules-load,
# zfs-load-module and zfs-mount (masked) and zed (started after the
# modprobe), and what modules-load.d lists, which a masked boot skipped
vm 'timeout 60 systemctl is-system-running --wait >/dev/null 2>&1; systemd-analyze; systemd-analyze blame | awk "!/\\.device\$/ && ++n <= 15"
  echo "systemd-udev-settle: $(systemctl is-active systemd-udev-settle.service), $(systemctl is-enabled systemd-udev-settle.service 2>&1); zfs-import-scan: $(systemctl is-enabled zfs-import-scan.service 2>&1)"
  echo "wanted by: $(systemctl show -p WantedBy -p RequiredBy --value systemd-udev-settle.service | tr "\n" " ")"
  for u in systemd-modules-load zfs-load-module zfs-mount zfs-zed; do printf "%s: %s, %s; " $u "$(systemctl is-active $u.service)" "$(systemctl is-enabled $u.service 2>&1)"; done; echo
  echo "modules-load.d (not loaded at boot when masked): $(cat /etc/modules-load.d/*.conf /run/modules-load.d/*.conf /usr/lib/modules-load.d/*.conf 2>/dev/null | awk "!/^[#;]/ && NF" | tr "\n" " ")"
  echo "failed units: $(systemctl --failed --plain --no-legend | awk "{ print \$1 }" | tr "\n" " ")"
  echo "sshd penalties: $(sudo sshd -T 2>&1 | awk "tolower(\$0) ~ /^persourcepenalt/" | tr "\n" " ")"
  sudo journalctl -b -u ssh.service -u ssh.socket --no-pager -o short-monotonic 2>/dev/null | awk "/penalt|nologin|not permitted|Failed|Connection closed|Accepted/ && ++n <= 12"
  systemd-analyze critical-chain ssh.service nfs-server.service 2>&1 | awk "NR <= 40"
  echo "mounted under /boot: $(findmnt -rn -o TARGET | awk "/^\/boot/" | tr "\n" " ")"' \
  >"$work/systemd-analyze.txt" 2>&1 &
analyze=$!
wait "$build" || { echo "building the test binaries failed" >&2; exit 1; }
build=
bins=()
for pkg in "${pkgs[@]}"; do bins+=("$work/$pkg.test"); done
has endpoint && bins+=("$work/endpoint.plan")
[ -z "$unit" ] || bins+=("$work/zfs-endpoint" "$work/unitprobe" ci/real-zfs-unit.sh deploy/systemd/zfs-endpoint@.service \
  deploy/systemd/zfs-endpoint@.service.d/hardening.conf deploy/systemd/zfs-endpoint-reconcile@.service)
scp "${common[@]}" -P 2222 "${bins[@]}" ci/real-zfs-guest.sh ci/test-times.awk ci@127.0.0.1:
rc=0
# concurrent shards on tmpfs-backed pools, three per suite (ci/real-zfs-guest.sh);
# the logs come back per shard and are merged per check below.
# ZFSDIFF_FULL=1 replays the whole zfsdiff corpus, ZFS_CRASH_FULL=1 runs
# every crash point on the real pool (both nightly.yml).
# Bounded here too: each shard's -test.timeout (4m, 12m for the full
# tiers; ZFSVM_TEST_TIMEOUT) cannot end a test stuck in the kernel (D
# state on a suspended pool), and the guest's wait for its shards would
# then hold this ssh until the job's own timeout, with no logs copied
# back. On the bound the guest's processes stay; out/ is copied as is
gt=4m ht=6m
[ -z "${ZFSDIFF_FULL:-}${ZFS_CRASH_FULL:-}" ] || { gt=12m ht=14m; }
timeout -k 10 "$ht" ssh "${common[@]}" -p 2222 ci@127.0.0.1 "sudo env ZFSVM_PKGS='${pkgs[*]}' ZFSVM_UNIT=$unit ZFSVM_EP_PART=$part ZFSVM_EP_PER=$per ZFSVM_TEST_TIMEOUT=$gt ZFSVM_RUN=${GITHUB_RUN_ID:-local}.${GITHUB_RUN_ATTEMPT:-1} ZFSVM_CSI_NFS=${ZFSVM_CSI_NFS:+1} ZFSDIFF_FULL=${ZFSDIFF_FULL:-} ZFS_CRASH_FULL=${ZFS_CRASH_FULL:-} bash real-zfs-guest.sh" || rc=$?
case $rc in 124 | 137) echo "::error::vm: the guest's test run did not end within $ht (shard timeouts $gt); the shard logs below are as far as they got"; timeout 30 ssh "${common[@]}" -p 2222 ci@127.0.0.1 'ps -eo pid,stat,etime,wchan:32,args --sort=start_time | awk "\$2 ~ /D/"' || true ;; esac
mkdir -p "$work/out"
timeout -k 5 120 scp "${common[@]}" -P 2222 'ci@127.0.0.1:out/*' "$work/out/" || rc=1
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
# what check-skips reads: endpoint = the union of this part's shards (it
# must cover endpoint.part.list); csi-mount = the non-sanity tests plus csi-sanity on
# filesystems (it must cover csi.list); csi-block = csi-sanity on zvols;
# csi-nfs (ZFSVM_CSI_NFS) = csi-sanity in NFS mode
# endpoint, split over parts: this part's tests (endpoint.part.list) and
# the other parts' (endpoint.other.list), from the plan; the guest's own
# list must be the host's, which the plan was made from
eplogs=()
if has endpoint; then
  lo=$((${part%/*} * per)) hi=$((${part%/*} * per + per))
  for i in $(seq "$lo" $((hi - 1))); do eplogs+=("$o/endpoint.$i.log"); done
  cat "${eplogs[@]}" >"$work/endpoint.log" || rc=1
  cmp -s "$o/endpoint.list" "$work/endpoint.list" ||
    { echo "::error::vm/endpoint: the guest's endpoint.list differs from the host's, which the plan was made from"; rc=1; }
  cp "$work/endpoint.plan" "$o/" || rc=1
  : >"$work/endpoint.part.list"
  : >"$work/endpoint.other.list"
  awk -v lo="$lo" -v hi="$hi" '$1 >= lo && $1 < hi { mine[$2] = 1 } { all[++n] = $2 }
    END { for (i = 1; i <= n; i++) { t = all[i]; if (seen[t]++) continue
            print t >(t in mine ? part : other) } }' part="$work/endpoint.part.list" other="$work/endpoint.other.list" "$work/endpoint.plan"
fi
if has csi; then
  cat "$o/csi-other.log" "$o/csi-mount.log" >"$work/csi-mount.log" || rc=1
  cp "$o/csi-block.log" "$o/csi.list" "$work/" || rc=1
  [ -z "${ZFSVM_CSI_NFS:-}" ] || cp "$o/csi-nfs.log" "$work/" || rc=1
fi
# the endpoint suite's reports. TestNFSReplay (internal/endpoint) replays a sharenfs corpus through the
# real libshare, exportfs and etab and diffs it against the reference
# models (internal/nfsref) and the simulator; the guest points its report
# at out/nfs-replay.log. Its last line is the tally.
inpart() { has endpoint && grep -qxF "$1" "$work/endpoint.part.list"; }
if ! inpart TestNFSReplay; then
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
if ! inpart TestZFSDiffReplay; then
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
# csi-sanity: the specs each shard passed (out/<shard>.passed) must be
# exactly the pinned list TestSanity also checks on the simulator
# (internal/csi/testdata/sanity-passed-<mode>.txt, ci/check-specs.sh): a
# spec that starts skipping or a new one fails by name, which the old count
# floor ("Ran >= 44") missed when one appeared as another went
# each suite's checks, in the job that runs it: the jobs together cover
# every real-pool package once (ci/check-suites.sh, in the static job)
if has endpoint; then
  # this part's tests and skips; an allowed skip planned for another part
  # is that part's to skip (SKIPS_ELSEWHERE)
  SKIPS_ELSEWHERE=$work/endpoint.other.list ci/check-skips.sh vm endpoint "$work/endpoint.log" "$work/endpoint.part.list" || rc=1
  # the plan a partition of endpoint.list over every part, each test in one
  # shard; this part's shard logs exactly its shards' tests, each once;
  # TestPropertyRoundTrip in every shard, a part of its rows each
  # (PROPRT_SHARD), every row in exactly one shard of all parts
  ci/check-split.sh TestPropertyRoundTrip "$work/endpoint.list" "$work/endpoint.plan" "$part" "${eplogs[@]}" || rc=1
fi
if has csi; then
  ci/check-skips.sh vm csi "$work/csi-mount.log" "$work/csi.list" || rc=1
  ci/check-skips.sh vm csi "$work/csi-block.log" || rc=1
  modes=(mount block)
  if [ -n "${ZFSVM_CSI_NFS:-}" ]; then
    ci/check-skips.sh vm csi "$work/csi-nfs.log" || rc=1
    modes+=(nfs)
  fi
  for m in "${modes[@]}"; do
    ci/check-specs.sh vm "csi-$m" "$o/csi-$m.passed" "internal/csi/testdata/sanity-passed-$m.txt" || rc=1
  done
fi
exit $rc
