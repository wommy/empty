# Real-ZFS integration tests in GitHub Actions

## EDGES (boundary and what crosses it)

- Hosted ubuntu-24.04 runners can run real ZFS with no self-hosted hardware. openebs/zfs-localpv `bdd-tests` is `runs-on: ubuntu-24.04` (github:openebs/zfs-localpv@8759e507 .github/workflows/tests.yml:37-38). The workflow exists; I did not check a run log, so this is intent, not proof of a passing run.
- The pool is a file vdev on a loop device. `ci/ci-test.sh:178-179`: `truncate -s 100G /tmp/disk.img`, then `sudo zpool create zfspv-pool "$(sudo losetup -f /tmp/disk.img --show)"`.
- Kubernetes runs on the host (`driver: none`, `.github/actions/setup-minikube/action.yml:36`), so the driver uses host ZFS directly.
- zrepl uses CircleCI `machine` VMs, not containers: `.circleci/config.yml:314-316` (`image: ubuntu-2404:2025.09.1`). It builds OpenZFS from source per release (`install-zfs-from-source`, lines 54-121).
- democratic-csi uses Vagrant (`Vagrantfile:12`, `ubuntu/jammy64`; `CONTRIBUTING.md:130`). Its ZFS sanity jobs run on self-hosted runners (`.github/workflows/main.yml:58-70`).

## SEAMS (interfaces to cut, best implementation, license)

- **Pool lifecycle.** zrepl `internal/platformtest/platformtest_zpool.go:47-95` (`CreateOrReplaceZpool`) exports an existing pool, recreates the image file, then runs `zpool create -f`. `Validate` (lines 30-45) requires an absolute image path and mountpoint. License: MIT.
- **Command runner.** zrepl `Execer` (`platformtest_exec.go`, `RunExpectSuccessNoOutput`) is an injectable runner, matching the decided Runner seam.
- **CSI conformance.** csi-test `pkg/sanity/sanity.go:69-72` documents `Address` as a gRPC endpoint such as `unix:/tmp/csi.sock`. `sanity.go:195,215,223` expose `NewTestConfig`, `NewTestContext`, `Test(t, config)`. License: Apache-2.0.
- **Go test helper.** go-zfs `test_helpers_test.go:67-100` builds a file-backed pool in a temp dir. License: unverified.

## Answers

1. Projects use loop or file vdev pools on hosted Ubuntu (openebs), or VM executors (zrepl). I found no privileged-container pool setup. Incus runs `zpool create` on loop devices (`test/suites/storage_driver_zfs.sh:95`); its CI runner is unverified.
2. Hosted ubuntu-24.04 works. The steps are `sudo apt-get install zfsutils-linux -y` and `sudo zfs version` (`setup-minikube/action.yml:20-25`), plus the `truncate`/`zpool create` above.
3. Isolation is by name and teardown. zrepl reserves the pool name `zreplplatformtest` (README:65). openebs uses `zfspv-pool` with `cleanup_loop_zfs` (`ci-test.sh:53-61`) and a `cleanup()` trap (`:63-94`). go-zfs cleans up with `pool.Destroy()` and `RemoveAll` (`test_helpers_test.go:96-99`). zrepl keeps the pool on failure only when `-failure.stop-and-keep-pool` is set (Makefile:297, commented out by default).
4. csi-sanity needs no cluster if the test calls the Go API with `unix:` (csi-test `sanity.go:69-72`, `:223`). openebs's `ci/sanity.sh:55-59` needs a kubelet socket because it runs the sanity binary against the cluster. Not run here (unverified end to end).

## GAPS

- No project I checked runs csi-sanity against a Go CSI front end over a unix socket on hosted runners in one job. openebs needs minikube; democratic-csi needs self-hosted runners or Vagrant; zrepl needs CircleCI VMs.
- Suggested shape: hosted ubuntu-24.04, `apt install zfsutils-linux`, a file vdev with a reserved pool-name prefix, a teardown trap that destroys the pool, and `sanity.Test` on `unix:` in the same job.
