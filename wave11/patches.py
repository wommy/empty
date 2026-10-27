import difflib, os
W = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave11"
A = W + "/work/a"
state = {}
def cur(p):
    if p not in state:
        state[p] = open(os.path.join(A, p)).read() if os.path.exists(os.path.join(A, p)) else None
    return state[p]
def patch(name, edits=(), newfiles=None):
    out = []
    for path, reps in edits:
        old = cur(path); new = old
        for o, n in reps:
            assert new.count(o) == 1, (name, path, o[:70], new.count(o))
            new = new.replace(o, n)
        out += difflib.unified_diff(old.splitlines(True), new.splitlines(True), "a/" + path, "b/" + path)
        state[path] = new
    for path, content in (newfiles or {}).items():
        out += difflib.unified_diff([], content.splitlines(True), "/dev/null", "b/" + path)
        state[path] = content
    open(f"{W}/build-patches/{name}", "w").write("".join(out))
    print(name, sum(1 for l in out if l[:1] in "+-" and not l.startswith(("+++","---"))), "changed lines")

CI = ".github/workflows/ci.yml"; SH = "ci/real-zfs-vm.sh"

# 01: install qemu in the background, overlapped with setup-go, image resolve and guest restore
patch("01-vm-apt-in-background.patch", [(CI, [
("""      - id: go
        uses: ./.github/actions/setup-go
        with:
          # its own namespace""",
"""      - name: start qemu install
        # apt (13-14s in every run so far, 3-6s of it a silent gap inside
        # `apt-get update`) needs nothing from setup-go, the image key or
        # the guest restore: run it behind them, wait for it before the VM
        run: |
          rm -f "$RUNNER_TEMP/apt.rc"
          setsid nohup bash -c '{ sudo apt-get update -o Acquire::Languages=none && sudo NEEDRESTART_SUSPEND=1 apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils cloud-image-utils; } >"$1/apt.log" 2>&1; echo $? >"$1/apt.rc"' _ "$RUNNER_TEMP" >/dev/null 2>&1 &
      - id: go
        uses: ./.github/actions/setup-go
        with:
          # its own namespace"""),
("""      - name: install qemu
        run: |
          sudo apt-get update
          sudo apt-get install -y --no-install-recommends qemu-system-x86 qemu-utils cloud-image-utils
          # the cloud image signing key ships in ubuntu-keyring on 24.04
          test -s /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg
""", ""),
("""      - name: test (Ubuntu 26.10, real zfs 2.4)
""",
"""      - name: wait for the qemu install
        run: |
          for _ in $(seq 480); do [ -e "$RUNNER_TEMP/apt.rc" ] && break; sleep 0.25; done
          cat "$RUNNER_TEMP/apt.log" || true
          test "$(cat "$RUNNER_TEMP/apt.rc" 2>/dev/null)" = 0 || { echo "::error::qemu install failed or did not finish"; exit 1; }
          # the cloud image signing key ships in ubuntu-keyring on 24.04
          test -s /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg
      - name: test (Ubuntu 26.10, real zfs 2.4)
"""),
])])

# 02: guest cache keyed by the provisioning spec only; one verified SHA256SUMS; validate before storing
USERDATA = """#cloud-config
# The provisioned guest is cached by the image's sha256 and a hash of this
# directory (ci/guest/). Anything that changes what is on the guest disk
# belongs here; ci/real-zfs-vm.sh can change freely without a cold run.
users:
  - name: ci
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys: [@PUBKEY@]
package_update: true
packages: [zfsutils-linux, nfs-kernel-server]
runcmd:
  - [modprobe, zfs]
  - [systemctl, enable, --now, nfs-server]
  # a cached guest boots days after it was provisioned: keep apt's timers
  # from upgrading zfsutils past the loaded kmod, or loading the guest,
  # while the tests run
  - [sh, -c, "systemctl disable --now apt-daily.timer apt-daily-upgrade.timer unattended-upgrades.service || true"]
"""
patch("02-guest-cache-key-and-validation.patch", [
(CI, [
("""          f=stonking-server-cloudimg-amd64.img
          sum=$(curl -fsSL --retry 3 https://cloud-images.ubuntu.com/stonking/current/SHA256SUMS | awk -v f="$f" '$2 == f || $2 == "*" f { print $1 }')
          test -n "$sum"
          echo "key=zfsvm-stonking-$sum-${{ hashFiles('ci/real-zfs-vm.sh') }}" >>"$GITHUB_OUTPUT\"""",
"""          # fetch and verify the list once; the script reuses it, so the key
          # and the image the script boots come from the same list even when
          # current/ moves between the two
          f=stonking-server-cloudimg-amd64.img
          base=https://cloud-images.ubuntu.com/stonking/current
          mkdir -p "$RUNNER_TEMP/zfsvm" && cd "$RUNNER_TEMP/zfsvm"
          curl -fsSL --retry 3 -o SHA256SUMS "$base/SHA256SUMS"
          curl -fsSL --retry 3 -o SHA256SUMS.gpg "$base/SHA256SUMS.gpg"
          gpgv --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg SHA256SUMS.gpg SHA256SUMS
          sum=$(awk -v f="$f" '$2 == f || $2 == "*" f { print $1 }' SHA256SUMS)
          test -n "$sum"
          # keyed by what is on the guest disk, not by the test driver: the
          # script changed 9 times in two hours, the provisioning spec twice
          echo "key=zfsvm-stonking-$sum-${{ hashFiles('ci/guest/**') }}" >>"$GITHUB_OUTPUT\""""),
]),
(SH, [
("""curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$base/SHA256SUMS"
curl -fsSL --retry 3 -o "$work/SHA256SUMS.gpg" "$base/SHA256SUMS.gpg"
""",
"""# the workflow fetches the list to key the guest cache: reuse it, verified
# again here, so the key and the image cannot come from different lists
[ -s "$work/SHA256SUMS" ] || curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$base/SHA256SUMS"
[ -s "$work/SHA256SUMS.gpg" ] || curl -fsSL --retry 3 -o "$work/SHA256SUMS.gpg" "$base/SHA256SUMS.gpg"
"""),
("""  cp "$cache/key" "$cache/key.pub" "$cache/seed.img" "$work/"
""",
"""  cp "$cache/key" "$cache/key.pub" "$cache/seed.img" "$work/"
  chmod 600 "$work/key"
"""),
("""cat >"$work/user-data" <<EOF
#cloud-config
users:
  - name: ci
    sudo: ALL=(ALL) NOPASSWD:ALL
    shell: /bin/bash
    ssh_authorized_keys: [$(cat "$work/key.pub")]
package_update: true
packages: [zfsutils-linux, nfs-kernel-server]
runcmd:
  - [modprobe, zfs]
  - [systemctl, enable, --now, nfs-server]
EOF
""",
"""# what goes on the guest disk lives in ci/guest/ (the cache key hashes it)
sed "s|@PUBKEY@|$(cat "$work/key.pub")|" ci/guest/user-data.in >"$work/user-data"
"""),
("""if [ -z "$warm" ] && [ -n "$cache" ]; then
""",
"""# store only a guest whose packages are installed: cloud-init writes
# boot-finished after a failed apt too, and a stored half-provisioned guest
# would fail every warm run until the key changes, where a cold run retries
guest_ok() {
  vm 'test "$(dpkg-query -W -f="\\${db:Status-Abbrev}\\n" zfsutils-linux nfs-kernel-server 2>/dev/null | grep -c "^ii")" = 2'
}
if [ -z "$warm" ] && [ -n "$cache" ] && guest_ok; then
"""),
("""  for _ in $(seq 1 60); do kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null || break; sleep 1; done
  if ! kill -0 "$(cat "$work/qemu.pid")" 2>/dev/null; then""",
"""  # qemu removes its pidfile on exit, so a failed cat means powered off
  for _ in $(seq 1 60); do kill -0 "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null || break; sleep 1; done
  if ! kill -0 "$(cat "$work/qemu.pid" 2>/dev/null)" 2>/dev/null; then"""),
]),
], newfiles={"ci/guest/user-data.in": USERDATA})

# 03: phase timestamps (root-causes the 12s and 1.8-5.1s gaps seen in the logs)
patch("03-vm-phase-timestamps.patch", [(SH, [
("""set -euo pipefail
""",
"""set -euo pipefail
# phase marks: the job log only timestamps lines, and several phases print
# nothing (the boot, the compile, the cloud-init wait)
mark() { echo "[zfsvm +${SECONDS}s] $*"; }
"""),
("""[ -n "$sum" ] || { echo "$file is not in $base/SHA256SUMS" >&2; exit 1; }
""",
"""[ -n "$sum" ] || { echo "$file is not in $base/SHA256SUMS" >&2; exit 1; }
mark "checksum list verified"
"""),
("""if [ -n "$warm" ]; then overlay; else boot "$img"; fi
""",
"""mark "image ready"
if [ -n "$warm" ]; then overlay; else boot "$img"; fi
mark "qemu started"
"""),
("""wait_ssh
# status exits non-zero""",
"""mark "test binaries built"
wait_ssh
mark "ssh up"
# status exits non-zero"""),
("""[ -n "$done_" ] || { echo "cloud-init never finished" >&2; exit 1; }
""",
"""[ -n "$done_" ] || { echo "cloud-init never finished" >&2; exit 1; }
mark "cloud-init done"
"""),
("""vm 'systemctl is-system-running --wait >/dev/null || true'
""",
"""vm 'systemctl is-system-running --wait >/dev/null || true'
mark "guest settled"
vm 'systemd-analyze blame 2>/dev/null | head -n 8' || true
"""),
("""scp "${common[@]}" -P 2222 "${bins[@]}" ci@127.0.0.1:
""",
"""mark "checks passed"
scp "${common[@]}" -P 2222 "${bins[@]}" ci@127.0.0.1:
"""),
])])

# 04: run ci on a schedule on the default branch so its cache scope (readable by every PR) holds
# the Go caches and the provisioned guest; nothing writes that scope today
patch("04-warm-default-branch-scope.patch", [(CI, [
("""on:
  push:
    branches: [main]
  pull_request:
""",
"""on:
  push:
    branches: [main]
  pull_request:
  # Caches saved by a pull_request run are scoped to refs/pull/N/merge: no
  # other PR, and not the default branch, can restore them. A PR restores
  # its own scope, its base branch's and the default branch's. Nothing ran
  # on the default branch, so every new PR (and every new image serial)
  # started cold. A daily run there keeps that scope warm for all PRs and
  # provisions the guest for a new current/ image before a PR needs it.
  schedule:
    - cron: "43 2 * * *"
  workflow_dispatch:
"""),
])])

# 05: product bug surfaced by CI ("Terminate orphan process: pid (...) (sleep)" in every test job)
patch("05-localrunner-kill-process-group.patch", [("internal/zfs/runner.go", [
("""	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
""",
"""	// signal the child's own process group (childAttr sets Setpgid), not
	// just the leader: a grandchild holding a pipe otherwise outlives Run
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
"""),
("""	err := cmd.Run()
""",
"""	err := cmd.Run()
	if ctx.Err() != nil && cmd.Process != nil {
		// WaitDelay's SIGKILL reaches only the leader; reap the rest
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
"""),
])])

# 06: cold path only: overlay instead of resizing the downloaded image in place
patch("06-cold-overlay-not-resize.patch", [(SH, [
("""  qemu-img resize "$img" 20G
""",
"""  # an overlay, not `qemu-img resize` in place: resizing the image curl
  # just wrote took 1.8-5.1s (it flushes the fresh 877 MB file)
  qemu-img create -q -f qcow2 -F qcow2 -b "$img" "$work/prov.qcow2" 20G
"""),
("""if [ -n "$warm" ]; then overlay; else boot "$img"; fi
""",
"""if [ -n "$warm" ]; then overlay; else boot "$work/prov.qcow2"; fi
"""),
("""    qemu-img convert -O qcow2 "$img" "$cache/disk.qcow2"
""",
"""    qemu-img convert -O qcow2 "$work/prov.qcow2" "$cache/disk.qcow2"
"""),
("""    sleep 2
    boot "$img"
""",
"""    sleep 2
    boot "$work/prov.qcow2"
"""),
])])
