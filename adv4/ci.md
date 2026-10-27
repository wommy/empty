# Adversarial review: CI / build / deps (HEAD bdf89d0)

Evidence sources: files at HEAD; GitHub job logs of run 40448591249 (commit ba26fd8, last green; it still ran Go 1.24.7 and csi-test v5.4.0, i.e. it predates the 1.27 bump and the file-export merge). Run 40450338099 (HEAD) was still in progress; runs 13 and 15 failed (causes not inspected).
Nothing was executed against the repo; the repo was not modified.

## Ranked findings

### 1. HIGH - NFS file-export contract tests silently SKIP on both real-ZFS jobs; the newest feature is never verified on real ZFS
- internal/endpoint/export_contract_test.go:15-26 (`h.NFS(t)` does `t.Skip` when /proc/fs/nfsd/threads is 0/absent); callers: TestFileExportLifecycle (:67), TestFileExportAdoptedVolumes (:227).
- ci.yml:69-72 (noble) and ci/real-zfs-vm.sh:38 (VM) install zfsutils-linux only. Run 12's apt log shows `nfs-kernel-server` listed under "Suggested packages", not installed. The commit message of the file-export merge says "zfs set exits 0 even when exportfs fails, every change is verified against /var/lib/nfs/etab" - exactly the behaviour only a real kernel can check.
- Nothing counts skips: a green job with 2 (or 20) skips is indistinguishable from full coverage.
- Fix:
```
# ci.yml noble
-          sudo apt-get install -y zfsutils-linux
+          sudo apt-get install -y zfsutils-linux nfs-kernel-server
+          sudo systemctl start nfs-server   # nfsd threads > 0
# real-zfs-vm.sh
-packages: [zfsutils-linux]
+packages: [zfsutils-linux, nfs-kernel-server]
# and add a skip budget, see #3
```

### 2. HIGH - No assertion of the ZFS version (userland or kmod) that is claimed; and the evidence shows userland/kmod mismatch already exists
- ci/real-zfs-vm.sh:58 prints `zfs version` but nothing checks it; ci.yml:38-41 and script header claim "OpenZFS 2.4.x" (also README/TrueNAS 27 claim).
- Real data: noble job log prints `zfs-2.2.2-0ubuntu9.5` / `zfs-kmod-2.3.4-1ubuntu2` (userland 2.2.2 against the Azure kernel's 2.3.4 module). So the "noble" job is a mixed-version configuration, not "the distro ZFS (2.2.x)".
- VM: the zfs version is whatever `current` image + apt resolves today (stonking is a pre-release series; the pocket moves; userland and kmod come from different packages: linux-modules vs zfsutils-linux). A silent 2.3.x/2.4.x flip or a 26.10 -> 2.5 bump still goes green. If the image kernel lacks zfs.ko (e.g. a -kvm flavour), `zfs version` still exits 0 with just userland printed (it warns about the missing module), so the failure only shows later at zpool create - loud but misattributed.
- Fix (script, after line 58; make expected versions parameters):
```
-vm 'cloud-init status --wait >/dev/null || true; zfs version; uname -r'
+want=${3:-2.4}
+vm 'cloud-init status --wait >/dev/null || true; cat /var/log/cloud-init-output.log | tail -n 30; uname -r' 
+ver=$(vm 'zfs version')            # two lines: zfs-X / zfs-kmod-Y
+echo "$ver"
+echo "$ver" | grep -Eq "^zfs-$want\." && echo "$ver" | grep -Eq "^zfs-kmod-$want\." \
+  || { echo "expected OpenZFS $want.x userland+kmod" >&2; exit 1; }
```
  and for noble pass want=2.2 (or assert userland==kmod major.minor and record). Also write the version into $GITHUB_STEP_SUMMARY.

### 3. HIGH/MED - A real-ZFS job can pass while exercising the simulator or nothing; no "really on ZFS" canary
- The harnesses fall back to the simulator when `ZFS_TEST_POOL==""`: internal/endpoint/endpoint_test.go:59-70, internal/csi/harness_test.go:30. Today each invocation sets it (ci.yml:82,87; script :66-70) so currently OK, but one dropped env (sudo env_reset - note script line 66 relies on `sudo VAR=val`, which works only because sudoers `ALL` implies SETENV; `sudo -E` in ci.yml:83,90 relies on preserve-env) turns the job into a duplicate of `unit`, green. Nothing detects it.
- Skips: `h.Sim(t)` skips are expected on real ZFS (3 in log), NFS skips (finding 1) are not; csi-sanity reports "41 Passed, 50 Skipped" in both jobs with no floor.
- Fix A (cheap, in test harnesses): `if os.Getenv("ZFS_REQUIRE_REAL")!="" && pool=="" { t.Fatal("simulator fallback forbidden") }` and set `ZFS_REQUIRE_REAL=1` in both jobs.
- Fix B (script canary, before destroy at :72): prove datasets were really created in the kernel pool:
```
  n=$(sudo zpool history zfsep-test | grep -c "zfs create")
  [ "$n" -gt 50 ] || { echo "only $n creates in pool history; simulator fallback?"; exit 1; }
```
- Fix C (skip budget): run with `-test.v 2>&1 | tee out.$pkg` and `[ "$(grep -c -- '--- SKIP' out.$pkg)" -le N ]`, or use gotestsum --junitfile and fail on unexpected skips.

Verified OK (checked, not findings): the VM remote script has `set -e`, `|| rc=$?` only accumulates (never resets rc), the final `exit $rc` propagates through ssh; ssh failure/VM death gives 255; `zpool create` failure aborts under set -e; missing zfs binary -> `zfs version` exit 127 aborts. `cloud-init ... || true` (:58) is tolerable *because* `zfs version` and `zpool create` follow, but see finding 2 for the module-missing case. The ci.yml csi loop (:89-91) is under `bash -e`, so a failing iteration exits immediately (no `pipefail` needed, no pipes). `if: always()` destroy with `|| true` is cleanup only.

### 4. HIGH (recurring failure) - Generated-code check depends on whatever plugin version/toolchain `go install` happens to build
- ci.yml:31-33: buf/protoc-gen-go/protoc-gen-go-grpc installed with `go install ...@version` using the job's Go; output formatting (go/format doc-comment rewrite) varies by the building toolchain -> the failure you hit (16792b4 failed, bdf89d0 "Regenerate gen/ with plugins built by Go 1.27").
- Local vs CI drift: any developer with plugins installed under an older Go regenerates different comments; gen headers record `protoc-gen-go v1.36.6` (gen/zfsendpoint/v1/endpoint.pb.go:3) while go.mod requires runtime protobuf v1.36.12 (go.mod:12). Not a compile error (runtime newer than gencode is allowed; only gencode newer than runtime is rejected by protoimpl.EnforceVersion), but you generate with a 6-patch-old generator that emits a different descriptor/layout than the runtime ships, and any bump of one without the other drifts silently. Module cache here has both protobuf@v1.36.6 and v1.36.12 (and grpc 1.5.1/1.68/1.72/1.84), confirming accumulated drift.
- Robust fix: make the plugins part of go.mod (pinned by go.sum, built by the go.mod toolchain on every machine, always equal to the runtime version) and run gofmt on the output so formatting no longer depends on the plugin build at all:
```
# one-time
go get -tool google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.12
go get -tool google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1      # separate module, pinned in go.mod/go.sum
# buf.gen.yaml
 plugins:
-  - local: protoc-gen-go
+  - local: [go, tool, protoc-gen-go]
-  - local: protoc-gen-go-grpc
+  - local: [go, tool, protoc-gen-go-grpc]
# ci.yml (and a `make gen` / go:generate shared with developers)
-          go install github.com/bufbuild/buf/cmd/buf@v1.47.2
-          go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.6
-          go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
+          go run github.com/bufbuild/buf/cmd/buf@v1.47.2 lint        # or: go get -tool buf too, but its dep tree is large; or use bufbuild/buf-action pinned by SHA
+          go run github.com/bufbuild/buf/cmd/buf@v1.47.2 generate
+          gofmt -w gen
+          git diff --exit-code -- gen/
```
  The `toolchain go1.27.3` line already forces every dev's `go` to the same compiler (GOTOOLCHAIN=auto), which is what makes `go tool` deterministic; running `gofmt -w gen` additionally removes the plugin-build sensitivity, and the existing `gofmt -l .` step (ci.yml:20) already requires gen/ to be gofmt-clean under the same toolchain. Expect one regeneration commit when moving 1.36.6 -> 1.36.12 (descriptor/`unsafe` string changes). `buf remote plugins` is a poorer choice: needs network + BSR account/rate limits, adds an unpinned-by-go.sum third party. Add `buf.lock`/pin buf itself the same way (buf.yaml has no deps so no lock needed).

### 5. MED - Ubuntu cloud image downloaded and booted with no checksum/signature verification; `current` is a moving target
- ci/real-zfs-vm.sh:25-27: `curl -fsSL ... "$build"=current`, then `qemu-img resize` and boot. TLS only; no SHA256SUMS/GPG check. The image is then given `NOPASSWD` root with our SSH key and runs fork-supplied test binaries, but more importantly a corrupted/poisoned or half-published `current` (daily/RC series, `stonking`) changes the test subject with no record.
- Fix:
```
base="https://cloud-images.ubuntu.com/$series/$build"
f="$series-server-cloudimg-amd64.img"
curl -fsSL --retry 3 -o "$img" "$base/$f"
curl -fsSL --retry 3 -o "$work/SHA256SUMS" "$base/SHA256SUMS"
curl -fsSL --retry 3 -o "$work/SHA256SUMS.gpg" "$base/SHA256SUMS.gpg"
gpgv --keyring /usr/share/keyrings/ubuntu-cloudimage-keyring.gpg "$work/SHA256SUMS.gpg" "$work/SHA256SUMS"  # apt: ubuntu-cloudimage-keyring
(cd "$work" && grep " \*\?$f\$" SHA256SUMS | sed "s# \*\?$f# $series.img#" | sha256sum -c -)
echo "image sha256: $(sha256sum "$img")" >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
```
  For true reproducibility pin a dated build (`stonking/20261020/`) and bump deliberately; keep a scheduled job on `current` to catch drift. (Dated dirs are eventually pruned upstream, so pair with caching below.)
- apt inside the VM (cloud-init `packages`) is unpinned and unrecorded beyond the version print; pin via `zfsutils-linux=<ver>` only if you want strict reproducibility, otherwise assert the minor (finding 2).

### 6. MED - Actions pinned by mutable tag; token left in checkout; PR code runs as root on the runner
- ci.yml:15,16,45,46,65,66: `actions/checkout@v4`, `actions/setup-go@v5` (log: "Node.js 20 is deprecated ... forced to run on Node.js 24"). Pin by full SHA with a version comment and let Dependabot (`package-ecosystem: github-actions`, plus `gomod`) bump them.
- `permissions: contents: read` (ci.yml:8-9) is good: no secrets are referenced anywhere; fork PRs get a read-only token and no secrets; hosted runners are ephemeral so KVM-on-fork is acceptable (note: first-time-contributor approval is the only gate; do not move this job to a self-hosted runner without `pull_request` hardening).
- Residual: checkout persists the GITHUB_TOKEN in .git/config (log shows `http.https://github.com/.extraheader`), and ci.yml:83,90 run PR-supplied code as root with `sudo -E go test`; token is read-only so impact is low. Fix: `with: {persist-credentials: false}` on all checkouts.
- `go install pkg@version` trust: resolved through GOPROXY and verified against sum.golang.org, so tampering is detectable, but nothing is pinned in go.sum; the `go tool` fix in #4 moves these into go.sum.

### 7. MED - `sudo -E go test` (noble job) compiles as root and can poison the Go caches
- ci.yml:83,90: root writes into the runner user's GOCACHE/GOMODCACHE (HOME preserved), leaving root-owned files; post-step `setup-go` cache save (miss case, which is every dependency bump such as this one, since the key is go.sum) can fail with EACCES or the cache gets root-owned artifacts. Also compiles twice more than needed.
- Fix: build as the user, run only the binary as root (same as the VM job):
```
go test -c -o "$RUNNER_TEMP/endpoint.test" ./internal/endpoint
sudo ZFS_TEST_POOL=zfsep-test "$RUNNER_TEMP/endpoint.test" -test.count=1 -test.v
```
  (Note ci.yml uses `./internal/endpoint/...`, the VM script uses exactly `endpoint server csi`; today `internal/endpoint` has no subpackages, but the two jobs' package sets can drift - derive both from `grep -rl ZFS_TEST_POOL` or one shared list.)

### 8. MED - VM failure diagnostics are thin
- script:51 only tails 80 lines of the serial console on failure; cloud-init's own log (`/var/log/cloud-init-output.log`, `cloud-init status --long`) is where apt failure is visible, and `cloud-init status --wait ... || true` (:58) discards the status. If ssh never comes up (loop :56 falls through silently after 450 s; the next `vm` call then fails with a generic ssh error), only the console tail exists.
- Fix: on failure also `vm 'cloud-init status --long; sudo tail -n 80 /var/log/cloud-init-output.log; dmesg | grep -i zfs'` (best-effort), explicit `|| { echo "VM never became reachable"; exit 1; }` after the wait loop, and upload $work/console.log with `actions/upload-artifact` `if: failure()`.

### 9. LOW/MED - Pool as large as the guest disk
- script:27,63: image resized to 20G and the vdev is a sparse 20G file on that same filesystem (which already holds the OS, ~3-5G used). ZFS believes it has 20G; a test that really writes data (or zvol thick reservations) can hit ENOSPC at the file layer and suspend the pool, a flaky failure that looks like a ZFS bug. Fix: `truncate -s 10G vdev0` or resize image to 30G.

### 10. LOW - Missing hygiene checks and CI controls
- No `concurrency` group: runs 12-16 show pushes in minutes of each other, each burning a KVM job. Add `concurrency: {group: ci-${{ github.ref }}, cancel-in-progress: true}`.
- No `timeout-minutes` on `unit` and `real-zfs-noble` (default 360); no `go mod tidy -diff`/`go mod verify`, no `govulncheck`; the 1.27/grpc 1.84 bump is not guarded against go.mod/go.sum drift. Add `go mod tidy -diff` (Go >=1.23) to unit.
- `go-version-file: go.mod` resolves the `toolchain` line (go1.27.3) - correct; but if the hosted tool-cache lacks that version it downloads each run (log of the old run shows 1.24.7 cached). Fine.
- Real-zfs noble is named for the "distro ZFS" but is userland 2.2.2 + kmod 2.3.4 (finding 2); rename or document.

## Speed / cost wins (from timestamps of run 12)
- unit: proto step 38 s of ~110 s, almost all compiling buf; after #4 use `go run ...@pinned` with GOCACHE hit or cache `~/go/bin` keyed on versions (or bufbuild/buf-action, SHA-pinned). Move proto lint/generate into its own job so it runs in parallel with test/fuzz (fuzz 36 s, race tests 20 s).
- real-zfs: whole job ~100 s: install qemu 16 s (`--no-install-recommends`, drop `cloud-image-utils` by writing the seed with `genisoimage`/`xorriso`, or keep), image download each run (cache `$work/$series.img` with `actions/cache` keyed on the SHA256SUMS hash from #5: saves the ~600 MB pull, and gives you an immutable pin), compile 3 test binaries serially (`go test -c` for the three packages could run with `&`/`wait`, or one `go test -c` each in parallel).
- csi package's non-sanity tests (TestCapabilitiesAdvertised, TestExpandNodeExpansionRequired, TestParametersAndAccessChecks) run twice (once per access type, seen twice in both logs): add `-test.run '^TestSanity$'` for the second iteration.
- Start the VM *before* compiling the test binaries (compile ~20 s overlaps with boot + cloud-init apt install ~40 s).
- `apt-get update` in noble + install zfs: unavoidable, ~12 s.

## Not an issue (checked)
- protoc-gen-go v1.36.6 gencode with runtime v1.36.12: allowed (runtime >= gencode), only drift, handled by #4.
- Trap (script:51): runs on EXIT incl. set -e failures and SIGTERM, kills qemu by pidfile; being installed after qemu starts is harmless on an ephemeral runner.
- Secrets exposure: none used; `permissions: contents: read`.
- `|| rc=$?` accumulation: correct, cannot mask a failure.
