# wave13 deploy builder report

Branch `deploy` (local, not pushed), based on claude/gallant-johnson-1qaxp5 @ 0a0bea3.
Worktree: /home/user/empty/.claude/worktrees/agent-af6bc4ba950ec770c

## Anomalies first

1. **Scope overlap with the zfs-endpoint builder.** Items 2 and 4 (one socket convention, `-version` on both binaries) cannot be done without editing cmd/zfs-endpoint/main.go. I kept the edits small and separate:
   - `-listen` default is now `""`, resolved by a new `listenAddr(listen, parent)` to `unixsock.EndpointAddr(parent)`;
   - a new `-version` flag, handled right after Parse;
   - a new `deps.stdout io.Writer` field (nil means os.Stdout);
   - the "serving" log line now logs the resolved socket and the version.

   The tests are in a new file, cmd/zfs-endpoint/deploy_test.go, so main_test.go is untouched. Expect a small textual conflict in the flag block and the `deps` struct when merging.
2. **zfs-csi no longer has a default `-endpoint`.** The old default (`endpoint.sock`) named a socket the unit never serves. zfs-csi cannot know the parent, so a default would name the wrong endpoint or none. It now takes `-parent <dataset>`, which derives `/run/zfs-endpoint/<systemd-escape parent>.sock`, or `-endpoint` as an override. Exactly one is required, otherwise it exits 1 with the convention in the message. This is a behaviour change for anyone who relied on the default; there was no working deployment that did.
3. **Block opt-in is a behaviour change.** It is off by default. An existing block StorageClass stops provisioning, and existing zvols are untouched. The real-VM `csi-block` sanity leg still runs block: `TestSanity` sets `EnableBlock` when `CSI_SANITY_ACCESS_TYPE=block`. Verified on the simulator: 44 of 103 specs, pass.
4. **ControllerGetCapabilities has no access-type capability in CSI.** So "GetCapabilities consistent" means CreateVolume and ValidateVolumeCapabilities agree: off, both refuse block, and Validate returns no Confirmed plus a message naming -enable-block. ControllerGetCapabilities is unchanged.
5. **No ldflags test in the suite.** Building a binary with `-ldflags -X` takes about 8s (link), which is over the ADR 0005 budget for one unit test. The `-version` tests prove the flag prints `version.Version`. I checked the -X symbol path by hand:
   `go build -ldflags "-X github.com/wommy/empty/internal/version.Version=v0.0.1-test" ./cmd/zfs-csi` then `zc -version` printed `v0.0.1-test`, rc=0.
   Follow-up: a CI static step that builds both binaries with -X and runs -version.
6. **No zfs-csi image exists.** deploy/k8s/controller.yaml names `ghcr.io/wommy/zfs-csi:v0.1.0` as a placeholder. There is no Dockerfile and no release workflow; gap A7 is still open for packaging.
7. **Sidecar versions are from memory.** csi-provisioner v5.2.0, csi-resizer v1.13.1 and csi-snapshotter v8.2.0 were not checked against the registry, because there is no network check here. Verify them, then pin by digest before production; the manifest says so.
8. **Other notes.**
   - go.mod: `go.yaml.in/yaml/v3` moved from indirect to direct, used only by the deploy test. go.sum is unchanged.
   - The hostPath `/run/zfs-endpoint` is mounted read-write. Read-only would likely still allow connect() on a socket, but I did not verify that, and a wrong guess would block startup.
   - No shell files were touched.

## Commits

| sha | subject |
|---|---|
| 0996a08 | unixsock: ListenShared for a kubelet emptyDir; one endpoint socket name |
| 2b31eed | version: one build version for both binaries, set by -ldflags -X |
| 38ef9b6 | csi: block access is opt-in (Controller.EnableBlock) |
| 9a9381a | cmd: zfs-csi serves in a 0777 emptyDir; -parent, -enable-block, -version |
| 356406e | deploy: Kubernetes manifests, pinned socket convention, ADR 0006 |

## What changed, per item

1. **emptyDir socket.** `unixsock.ListenShared` / `SharedDir` (internal/unixsock/unixsock.go) carry the security reasoning in the doc comment.
   - It relaxes only the group/other-write check on the directory.
   - These are still refused: a symlinked directory, a directory owned by another user, a live socket, a non-socket at the path, and abstract or relative paths. The socket is still 0600.
   - zfs-csi `listen()` uses it. zfs-endpoint and `Listen` are unchanged.
2. **Socket name.** The one convention is `/run/zfs-endpoint/<systemd-escape parent>.sock` (`unixsock.EndpointAddr`, `SystemdEscape`).
   - The unit already served this, so its ExecStart is unchanged and only its comment was updated.
   - zfs-endpoint's default `-listen` and `zfs-csi -parent` now resolve to the same path. README was updated.
   - deploy/deploy_test.go parses the unit's ExecStart and RuntimeDirectory and checks %i.sock against EndpointAddr.
3. **Block opt-in.** `Controller.EnableBlock` gates `kindOf` (CreateVolume returns InvalidArgument naming -enable-block) and `capSupported` (Validate does not confirm). zfs-csi has the new flag `-enable-block`, default false.
4. **Version.**
   - `internal/version.Version` is the single variable; the two "dev" constants are removed.
   - Both binaries have `-version`, which prints and exits 0.
   - Both "serving" logs include the version.
5. **deploy/k8s.** Files: csidriver.yaml, rbac.yaml (Namespace, ServiceAccount, ClusterRole, ClusterRoleBinding), controller.yaml (Deployment), classes.yaml (StorageClass, VolumeSnapshotClass).
   - Validated by `TestManifests` (go.yaml.in/yaml/v3), which checks:
     - every document decodes;
     - all required kinds are present;
     - the driver name is `zcsi.DefaultDriverName`;
     - attachRequired and podInfoOnMount are false;
     - the socket-dir is an emptyDir mounted at /csi in all 4 containers;
     - the sidecars use `--csi-address=/csi/csi.sock`;
     - zfs-csi has `-parent`, the hostPath is `unixsock.EndpointDir`, and block is not enabled in the example;
     - images are pinned to a `vX.Y.Z` tag.
   - Also cross-checked with python3 yaml.safe_load_all.
6. **ADR.** docs/adr/0006-first-deployment-shape.md: status accepted (decisive, reversible).

## Fail-before / pass-after evidence

I got the evidence by mutating the new code back to the old behaviour, running the test, and restoring the code (all were restored; the final tree is the committed one).

| Item | Test | Before (mutation = old behaviour) | After |
|---|---|---|---|
| 1 | cmd/zfs-csi TestListenInAKubeletEmptyDir | `listen` back to `unixsock.Listen` (exactly the base code): `FAIL ... /tmp/zfs-csi.../csi is writable by group or others (-rwxrwxrwx)` | ok |
| 1 | internal/unixsock TestListenSharedRelaxesOnlyTheDirectoryMode | new API; it also asserts that `Listen` still refuses 0777 | ok |
| 2 | deploy TestUnitServesTheConventionalSocket | unit `-listen` set to `endpoint.sock`: `unit serves unix:///run/zfs-endpoint/endpoint.sock for tank/k8s/vols, binaries use unix:///run/zfs-endpoint/tank-k8s-vols.sock` (x3) | ok |
| 2 | cmd/zfs-endpoint TestDefaultListenIsTheUnitSocket | default back to `endpoint.sock`: `default: "unix:///run/zfs-endpoint/endpoint.sock"` | ok |
| 2 | internal/unixsock TestSystemdEscape | expected values are real `systemd-escape` output (`tank-my\x2dvols`, `tank-a\x20b`, `tank-\xc3\xa9`, `\x2ex-y`) | ok |
| 3 | internal/csi TestBlockIsOptIn | CreateVolume gate removed: `block with block off: <nil>`; Validate gate removed: `block confirmed with block off: ""` | ok |
| 4 | cmd/zfs-endpoint and cmd/zfs-csi TestVersionFlag | flag removed (the base state): `flag provided but not defined: -version` (both) | ok |
| 5 | deploy TestManifests | new files | ok |

The new tests use t.Parallel and have no sleeps.

## Gate (final tree, 356406e)

- `gofmt -l .`: empty
- `go vet ./...`: clean
- `go mod tidy -diff`: clean
- `go test -race -count=1 ./...`: all ok
- `CSI_SANITY_ACCESS_TYPE=block go test -run '^TestSanity$' ./internal/csi/`: ok (Ran 44 of 103 specs)
- `bash -n`: no shell touched
