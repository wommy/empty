# EDGE: BLOCK EXPORT (zvol over LIO iSCSI and nvmet NVMe-oF)

## EDGES

- Inbound: gRPC from the ZFS endpoint daemon (export/unexport/deny-initiator, CHAP set). Input is a zvol path, initiator IQN/NQN, LUN.
- Outbound: kernel configfs under `/sys/kernel/config/target` (LIO) and `/sys/kernel/config/nvmet` (nvmet). Export state is persisted either as a saveconfig file or as controller-pushed desired state.
- Dependency: zvol existence comes from the ZFS edge. The block edge does not create zvols.
- Crossing: initiator identity (IQN/NQN), portal addresses, LUN number, CHAP secrets.

## SEAMS

1. **Export RPC surface.** targetd exposes `vol_*`, `export_create/destroy`, `initiator_set_auth`, `access_group_*` (`github:open-iscsi/targetd targetd/block.py:112-131`). Best fit for the RPC shape.
2. **LIO driver.** targetd uses the rtslib-fb Python library, not targetcli (`targetd/block.py:18-30`, configfs root at `github:open-iscsi/rtslib-fb rtslib/node.py:30`). Go has no rtslib port found in the searches run. Best Go implementation: `github:isac322/pillar-csi internal/agent/lio/target.go:664-696` (`ensureLUN`, `ensureMappedLUN`), which writes configfs directly.
3. **nvmet driver.** Best existing: `github:isac322/pillar-csi internal/agent/nvmeof/configfs.go` (read-back in `verifyPrepared`, line 1166). It was the only Go nvmet configfs writer surfaced.
4. **Zvol backend.** targetd shells to the `zfs` CLI (`targetd/backends/zfs.py:149-165`, `:317`). Other ZFS work belongs to the ZFS edge.
5. **Persistence.** Two models exist:
   - saveconfig: targetd calls `RTSRoot().save_to_file()` after each mutation (`block.py:270,312,334`). Boot restore is `targetctl restore` (`docker/launch:10`). rtslib default file is `/etc/target/saveconfig.json` (`rtslib/root.py:446`).
   - reconcile-on-start: pillar-csi's controller pushes full desired state through `ReconcileState(complete=true)`. The agent gates port-listening until then (`internal/agent/server_export_restore.go:24-43`). No saveconfig file.

## GAPS

- **No Go LIO library.** Searches found no Go rtslib equivalent with saveconfig. pillar-csi is the only direct configfs writer found.
- **gotgt and go-iscsi-helper are out of scope.** gotgt is a userspace SCSI target with a static JSON config read once at startup (`github:gostor/gotgt README.md:46`). go-iscsi-helper drives tgtd/tgtadm, not LIO (`iscsi/target.go:26`).
- **LUN numbering.** targetd takes `lun` from the caller and dedupes only the exact (ACL, LUN) pair (`block.py:263-268`). pillar-csi hardcodes LUN 0 per IQN (`lio/target.go:664`), so one IQN per volume.
- **Error codes.** targetd uses JSON-RPC-style negative codes (`targetd/utils.py:43-66`: NAME_CONFLICT -50, UNEXPECTED_EXIT_CODE -303, NOT_FOUND_VOLUME_EXPORT -151). Existence checking is visible only in LVM (`backends/lvm.py:188-190`). The zfs `create -V` path (`zfs.py:317`) shows no equivalent guard in the read.
- **Stale configfs after crash.** targetd saves only after a completed mutation, so a crash between kernel edit and save leaves the file behind kernel state. This is inference from the code, not a documented behavior.
- **democratic-csi.** It shells out to targetcli and nvmetcli (`github:democratic-csi/democratic-csi README.md:440,491-541`, `contrib/scale-nvmet-start.sh:27-31`). The mutex claim is unverified: `async-mutex` appears only in `src/driver/controller-synology/http/index.js:86-87`.
- **CHAP.** targetd sets CHAP fields in `initiator_set_auth` (`block.py:315-334`). pillar-csi has a read-back test (`lio/chap_test.go:147`). Neither handles CHAP drift after a crash.
