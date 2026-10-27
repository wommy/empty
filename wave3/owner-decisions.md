# Owner decisions for zfs-endpoint (revised: OpenZFS is the source of truth)

Owner principle: OpenZFS semantics are the source of truth. democratic-csi is one imperfect implementation and an optional adoption target, not the behavioural baseline. Each decision below states what OpenZFS's own model says first. Where a default existed only for democratic-csi parity, the OpenZFS-loyal option is recommended and the parity option is listed as an alternative.

OpenZFS model points used throughout:
- Ownership and metadata live in local user properties (`module:property`, e.g. `zfs-endpoint:owner`). Inherited values are not ownership (`internal/endpoint/table.go`, "ownership = LOCAL props only").
- Snapshots belong to their dataset. `zfs destroy` without `-r` refuses when snapshots exist; `-r` destroys them; `-d` defers destruction of snapshots that have holds or clones.
- `zfs create -V` requires an explicit size. A zvol has no default volsize.
- `sparse` (`zfs create -s`, refreservation none) is a zvol concept. A filesystem's space guarantee is `refreservation`. `reserve` on a filesystem therefore maps to an OpenZFS property that exists for both kinds.
- `sharenfs` and `sharesmb` are native ZFS properties, persisted by `zfs share -a` at boot.
- A zvol carries no filesystem. ext4 or xfs on a zvol is an OS decision, not a ZFS one.

Not checked in this pass: the exact user-property name grammar in the OpenZFS source. The owner should confirm that a hyphenated module name such as `democratic-csi` is valid before relying on it.

Urgency tiers: **blocks current work**, **soon** (before the next merge to main or first tag), **later**.

---

## Blocks current work

### 1. CreateVolume for a block volume with capacity 0: reject, or pick a default?

Question: when a block CreateVolume has no size, should the stack reject it or choose one?

OpenZFS model: `zfs create -V` requires a size. ZFS has no default volsize, so any default is a policy decision made above ZFS.

Options:
- A. Reject with InvalidArgument (OpenZFS-loyal, current `internal/endpoint/plan.go:95`). The user must set a size, as `zfs create -V` does.
- B. CSI layer supplies a default from explicit configuration (a flag or StorageClass value). Consequence: spec-tolerant at the CSI edge, and the number is the owner's choice rather than a copied constant.
- C. A hard-coded 1 GiB default, copied from democratic-csi (`src/driver/controller-zfs/index.js:732-738`). This is parity-only and has no ZFS reason behind it.

Recommended default: A. The OpenZFS model has no default size, and rejecting keeps the size visible to the user. If a default is wanted, make it configuration (B), not a copied number (C).

Urgency: **blocks current work**. The CSI front end is merged and this changes its CreateVolume contract.

### 2. `sparse` on a filesystem and `reserve` on a zvol: reject or ignore?

Question: should a parameter that does not apply to the requested kind fail, or be dropped?

OpenZFS model: `-s` is valid only for zvols. Filesystem space guarantees are `refreservation` (and `reservation`), which apply to both kinds. The ZFS tools reject the mismatched form rather than ignoring it, so reject follows OpenZFS.

Options:
- A. Reject (OpenZFS-loyal, current `plan.go:101-105`). A StorageClass that sets `sparse` must be used only for block PVCs. One StorageClass cannot serve both modes unless it omits the parameter.
- B. Ignore the mismatched parameter. Consequence: one class serves both modes, but a thin/thick intent is dropped silently.
- C. Map `reserve` on a zvol to `refreservation`, and `sparse` on a filesystem to nothing. This is closer to ZFS's own property model but adds an interpretation.

Recommended default: A, because it follows the ZFS tools. Option C is a reasonable refinement if the owner wants `reserve` to mean the same thing for both kinds.

Urgency: **blocks current work**. It changes the CreateVolume contract.

### 3. `fs_type` for mount volumes

Question: should `fs_type` other than `zfs` be accepted on a mount request?

OpenZFS model: a dataset is a filesystem and a zvol is a block device. Putting ext4 or xfs on a zvol is an OS-level choice, and no ZFS property or command manages it. zfs-localpv's `fstype=zfs` switch (dataset) versus anything else (zvol) is a convenience of that driver, not an OpenZFS rule.

Options:
- A. Accept only `zfs` (or empty) for mount volumes, as now (`internal/csi/controller.go:105-110`). This keeps the ZFS-native meaning: mount means a dataset.
- B. Accept ext4 or xfs on a zvol, as zfs-localpv does (`pkg/zfs/zfs_util.go:168-178`). Consequence: the controller creates a zvol and the node must format and mount it, which needs a node plugin (decision 11).
- C. Pass any `fs_type` through and let the node decide. The endpoint then validates nothing about it.

Recommended default: A. It is the ZFS-native meaning. Revisit B only when the node side exists and a user needs formatted zvols.

Urgency: **blocks current work**.

### 4. Remote (TCP) endpoint with no auth

Question: should the daemon listen on TCP before any transport authentication exists?

OpenZFS model: not applicable. This is a transport and trust question, and ZFS's own permission model (`zfs allow`) applies only to local commands run as a user, not to network clients.

Evidence: `cmd/zfs-endpoint/main.go:103-106` calls `net.Listen("tcp", addr)` for any address that is not `unix://`, with no authentication. The README says the socket is owner-only and transport auth is "not started".

Options:
- A. Unix socket only. Remove the TCP branch until auth exists. Consequence: front ends run on the host or mount the socket.
- B. Add mTLS now (per-front-end client certificates, or SPIFFE). Consequence: real authentication, plus per-dataset authorization, which must be built.
- C. Keep TCP, unauthenticated, behind a flag. Consequence: any reachable client can create and destroy volumes.

Recommended default: A. The current code exposes an unauthenticated write API, and the README already promises socket-only.

Urgency: **blocks current work**. It is a live exposure.

### 5. First export: NFS or block?

Question: which export edge is built first?

OpenZFS model: NFS has a native ZFS path. `sharenfs` is a dataset property, and `zfs share -a` re-applies it at boot. Block export has no ZFS path at all. A zvol is exported by an external target (LIO or nvmet), outside ZFS's model.

Options:
- A. NFS first, through `sharenfs` (OpenZFS-native). Open items: `sharenfs` options are uniform across clients, and an ancestor `sharenfs=on` exports new volumes to everyone (`docs/research/gaps/adv-file-export-design.md` finding 3). Both can be handled by a startup check and an explicit per-volume export call.
- B. Block first, through LIO or nvmet configfs. Consequence: three critical findings are open (`docs/research/gaps/adv-block-design.md` #1-#3), and a revised design exists only on a branch. It also needs a node plugin (decision 11).
- C. Both in parallel. Two large unknowns at once.

Recommended default: A. It is the ZFS-native path, and its design has been through two adversarial rounds.

Urgency: **blocks current work**. It sets the next implementation wave.

---

## Soon (before the next merge to main or the first tag)

### 6. Repo name and Go module path

Question: should `wommy/empty` and `github.com/wommy/empty` be renamed to match `zfs-endpoint`, and when?

OpenZFS model: not applicable. This is a project-identity decision.

Options:
- A. Keep the names. The README title and the repo name disagree.
- B. Rename the repo and module to `zfs-endpoint` before merge. Consequence: one mechanical change to `go.mod`, imports, `gen/`, and the issue-tracker docs. The cost grows with each commit.
- C. Rename after the first release. Consequence: a published module path then needs a redirect.

Recommended default: B, before the PR merges.

Urgency: **soon**.

### 7. v1 property allowlist for StorageClass parameters

Question: which OpenZFS properties may a StorageClass set at create?

OpenZFS model: the allowlist should be a subset of properties that `zfs set` or `zfs create -o` accepts, with values validated by ZFS. Some settable properties have serious side effects: `dedup` adds a dedup table whose memory cost scales with the pool, and OpenZFS documents it with warnings. The allowlist is a policy, so the owner should decide it.

Options:
- A. The current list (`endpoint.proto`, field 9): filesystem compression, recordsize, atime, relatime, sync, logbias, dedup, xattr, acltype, snapdir, copies; block compression, volblocksize, sync, logbias, dedup, copies.
- B. A smaller list that drops `dedup` and the rarely needed attributes (`xattr`, `acltype`, `snapdir`) until someone asks. Consequence: fewer knobs to test, and no casual dedup setting.
- C. Add encryption properties (decision 22). Requires the secrets decision first.

Recommended default: B. It is the OpenZFS-conservative choice: only settings with clear use, and dedup is the one a StorageClass should not set casually.

Open: zstd read-back and NFSv4 mapping are unverified on a real pool (checkpoint).

Urgency: **soon**.

### 8. CSI spec version and Go floor

Question: stay on CSI spec v1.12, or move to the newer spec with Go 1.25?

OpenZFS model: not applicable. This is a dependency decision.

Options:
- A. Stay on spec v1.12.0 (`go.mod`) and csi-test v5.4.0. No toolchain change. The docs must name the spec claimed.
- B. Move spec, csi-test and Go together. The local spec clone is v1.14.0 (`scratchpad/repos/spec/VERSION`). v1.14 removes `VolumeCondition`, which is harmless because health is not implemented. The checkpoint says csi-test v5.5 needs Go 1.25; this was not re-verified here, and the wave3 go-version scout output is not on disk.
- C. Move the spec and keep Go 1.24, if csi-test v5.4 works with a newer spec. Not checked.

Recommended default: B, as one change, with the spec version named in the docs.

Urgency: **soon**.

### 9. Ownership namespace and compatibility with democratic-csi

Question: which property prefix does the endpoint write, and what does it accept on volumes it did not create?

OpenZFS model: user properties use a `module:property` form, and the module name should belong to the tool that owns the value. For this project the natural module is `zfs-endpoint:`. The `democratic-csi:` prefix exists only because democratic-csi wrote it first. The default prefix (`cmd/zfs-endpoint/main.go:37`) is therefore a parity default, not an OpenZFS one. The adoption of existing volumes is a separate, optional concern.

Options:
- A. Own prefix by default (`zfs-endpoint:`), with adoption of `democratic-csi:*` as an explicit, opt-in mapping (a flag listing the prefixes to read). Consequence: the endpoint's ownership is clear and it does not claim volumes by default. Existing democratic-csi volumes need the flag to be adopted.
- B. Keep `democratic-csi:` as the default and write it. Consequence: the endpoint claims another tool's namespace, and volumes it creates look adopted by democratic-csi.
- C. Write both prefixes. Consequence: double the property writes and two ownership sources to keep consistent.

Recommended default: A. Rationale: ownership should be stated in the tool's own module namespace, and adoption should be a deliberate act.

Urgency: **soon**. It sets what every new volume carries.

### 10. Migration direction (adoption) between democratic-csi and this endpoint

Question: must volumes move in both directions?

OpenZFS model: adoption is a property read and, if wanted, a property rename. `zfs set` and `zfs inherit` do the rename directly. A reverse move is the same operation with the prefixes swapped. Nothing outside ZFS (LIO or NFS state, volume_context) moves with it.

Options:
- A. One-way: read the opt-in prefixes listed in decision 9. No migration tool. Moving back means recreating volumes or running a rename by hand.
- B. One-way adoption plus a documented `zfs set` recipe for renaming properties. Consequence: a manual but fully ZFS-native rollback.
- C. A two-way tool. Consequence: a large surface that must also reproduce democratic-csi's volume_context, which is not ZFS state.

Recommended default: B. It uses only OpenZFS operations and builds no tool.

Urgency: **soon**. It depends on decision 9.

### 11. Node plugin: in scope or delegated?

Question: does this repo build the CSI Node service?

OpenZFS model: not applicable. Mounting an NFS export or formatting a device is kernel and node work. ZFS only provides the dataset and its share property.

Options:
- A. Delegate NFS to csi-driver-nfs (`scratchpad/repos/csi-driver-nfs`), against the export path the endpoint returns. The sanity harness still needs a node skip or stub (`docs/research/gaps/csi-frontend.md`, section 2).
- B. Build a minimal NFS and mount node plugin here. A second binary and a second privilege boundary.
- C. Build a block node plugin (per-LUN detach, `docs/research/edges2/node-attach.md`). Large, and the CHAP-on-argv problem is unresolved in the surveyed connectors.

Recommended default: A for NFS, and defer block node work until decision 5's block design is revised.

Urgency: **soon**.

### 12. OpenZFS version floor

Question: what is the minimum OpenZFS the project supports?

OpenZFS model: the floor should be the lowest version whose features the endpoint uses and which a supported OS ships. The features in use (`destroy -d`, `zfs get -Hp`, `sharenfs`) are the criteria.

Options:
- A. Floor 2.2 (what Ubuntu 24.04 ships), target 2.4.4. CI currently runs 2.2.2 userland with a 2.3.4 kmod on noble, and 2.4.4 on the 26.10 VM (commit `ab8e9bf`). 2.2 is not tested as a matched kmod.
- B. Floor 2.3. Drops 2.2 without a test.
- C. Floor 2.4.4 only. Simplest testing, but excludes 24.04.

Recommended default: A, plus a matched 2.2 kmod CI job.

Urgency: **soon**.

### 13. Supported distributions

Question: which distributions does the project support and test?

OpenZFS model: not applicable beyond the OpenZFS versions in decision 12.

Options:
- A. Ubuntu only: 24.04 LTS and 26.10 once GA. Matches CI.
- B. Ubuntu plus Debian. Needs a test matrix that does not exist yet.
- C. Ubuntu plus RHEL-family with OpenZFS from EPEL. The NFS and samba paths differ (`docs/research/gaps/adv-file-export-facts.md`), and the test cost is high.

Recommended default: A.

Urgency: **soon**.

### 14. Deployment target

Question: does the endpoint run as a systemd unit on the ZFS host, or as a container?

OpenZFS model: the daemon must run where the pool is imported, with root for `zfs share` and configfs. A ZFS-native delegation (`zfs allow`) does not cover mount, so the daemon cannot be an unprivileged container that only holds delegations (`docs/research/edges2/privilege-tenancy.md`).

Options:
- A. systemd unit on the host, with hardening (`CapabilityBoundingSet`, `ProtectSystem`). The controller pod reaches the socket by hostPath, or over TCP once decision 4 allows it.
- B. Privileged container or DaemonSet on the host. A harder privilege boundary to reason about.
- C. Both, with the unit as the reference.

Recommended default: A.

Urgency: **soon**. It depends on decision 4.

### 15. License

Question: under what license is the project released?

OpenZFS model: OpenZFS is CDDL-1.0 and is not linked, so it does not constrain the choice. The CSI spec and csi-test are Apache-2.0.

Options:
- A. Apache-2.0. Matches the CSI ecosystem and includes a patent grant.
- B. MIT. Matches democratic-csi and zrepl. No patent grant.
- C. GPL-3.0. Blocks embedding in proprietary drivers. Only ideas (not code) were taken from GPL projects.

Evidence: no LICENSE file exists in the tree. The go-zfs license is unverified (`docs/research/edges/zfs-exec.md`).

Recommended default: A.

Urgency: **soon**, before any public tag.

### 16. Reconcile and orphan GC authority

Question: should v1 include a reconcile RPC, and may it delete?

OpenZFS model: ZFS's own state is the truth. Ownership is local user properties, so a dataset that carries the endpoint's property and is absent from the caller's desired set is an orphan candidate. Deletion uses ZFS's own rules: `destroy` refuses when snapshots or clones exist, and holds block destruction. A reconcile that deletes must respect those rules and never bypass them.

Options:
- A. No reconcile in v1. Crash leftovers stay until an operator removes them.
- B. Report-only at startup, plus an on-demand Reconcile that deletes only against a caller-supplied desired set (`docs/research/edges2/reconcile-gc.md`). Consequence: deletion is explicit and still obeys ZFS destroy rules.
- C. Automatic GC at startup. Rejected by the research: a crash-time bug can delete live data.

Recommended default: B. A reconcile branch is already in flight, so this may be half decided.

Urgency: **soon**.

---

## Later

### 17. TrueNAS JSON-RPC as a Runner backend

Question: should the `Runner` seam get a TrueNAS middleware backend?

OpenZFS model: the local `zfs` and `zpool` CLI is the interface to OpenZFS. A JSON-RPC backend puts the ZFS semantics inside a different system (TrueNAS middleware), which has its own ownership and error model.

Options:
- A. Local CLI only (`internal/zfs/runner.go`, `LocalRunner`).
- B. An SSH runner for agentless mode, which runs the same CLI remotely. Needs decision 4's auth model.
- C. A TrueNAS JSON-RPC runner.

Recommended default: A. Consider B later.

Urgency: **later**.

### 18. HA and fencing scope

Question: is high availability in scope?

OpenZFS model: `zpool multihost` protects import only and is not fencing (`docs/research/edges2/ha-failover.md`). Exclusivity across hosts is the HA layer's job. The channel-program fencing was dropped in commit `b381081`.

Options:
- A. Single host only. The per-parent `flock` gives exclusivity on one host.
- B. Pacemaker with STONITH and the resource agents. The endpoint must rebuild its state on start, which is not done.
- C. `zpool multihost` as the fence. Rejected by the research.

Recommended default: A, documented.

Urgency: **later**.

### 19. Release and versioning

Question: how are releases numbered, and when is the proto frozen?

OpenZFS model: not applicable.

Options:
- A. `v0.x` tags until the export and reconcile RPCs land, then `v1.0` with the proto frozen under `buf breaking` (`buf.yaml` already sets `breaking: use: FILE`).
- B. Calver.
- C. Tag `v1` now. Promises a frozen API the export RPCs will change.

Evidence: no VERSION, CHANGELOG, or release workflow in the tree. `buf` is not installed locally, so `buf breaking` is unverified.

Recommended default: A.

Urgency: **later**.

### 20. Keep the research docs in-repo?

Question: should `docs/research/` stay in the repository?

OpenZFS model: not applicable.

Options:
- A. Keep, and promote each decision to an ADR under `docs/adr/`.
- B. Move to a branch or wiki. Breaks links and makes the corrections harder to find.
- C. Delete after decisions. Loses the record that stops old claims returning.

Recommended default: A.

Urgency: **later**.

### 21. ControllerModifyVolume scope

Question: should v1 implement mutable parameters?

OpenZFS model: quota, refquota, volsize, refreservation and compression can change live. recordsize, copies and volblocksize have new-writes-only or create-only semantics. volsize shrink is unsafe (`docs/research/edges2/capacity-modify.md`).

Options:
- A. None in v1.
- B. A live-safe subset, rejecting create-only properties.
- C. The full set, including shrink.

Recommended default: A.

Urgency: **later**.

### 22. Encryption scope

Question: should the endpoint manage ZFS native encryption, and where do keys live?

OpenZFS model: encryption is a dataset property set at create (`encryption`, `keyformat`, `keylocation`). Keys are loaded from `keylocation` (prompt, file, or https), and the key material never belongs on argv (`docs/research/edges2/encryption.md`).

Options:
- A. None in v1.
- B. Support `keylocation=file` or `https` with the credential in a Kubernetes Secret reference.
- C. A passphrase in StorageClass parameters, as truenas-csi does. The passphrase then sits in Kubernetes objects.

Recommended default: A for v1. If added later, B, and never C.

Urgency: **later**.

### 23. Changed-block tracking (SnapshotMetadata)

Question: should the endpoint offer CSI SnapshotMetadata?

OpenZFS model: OpenZFS has no block-range API for zvols. The only exact source is the send stream (`docs/research/edges2/replication-cbt.md`, Q2).

Options:
- A. Out of scope.
- B. Serve it from send-stream parsing with bookmark-pinned bases. Costly, and feasibility is unverified.
- C. A ZFS block-range API. Does not exist.

Recommended default: A.

Urgency: **later**.

### 24. Append-only audit log for destructive operations

Question: should the endpoint record who asked for each destroy, outside the pool?

OpenZFS model: `zpool history -l` records the daemon's user, not the caller, so ZFS cannot answer "who asked" (`docs/research/edges2/observability-audit.md`).

Options:
- A. No audit log.
- B. An append-only log before and after each destroy, with the caller identity from gRPC metadata.
- C. Rely on `zpool history`. Does not identify the caller.

Recommended default: B, once a second front end can destroy.

Urgency: **later**.

---

## Changes from the first version, for the owner's review

- Decision 1: recommendation moved from a democratic-csi-matching 1 GiB default to rejection, because `zfs create -V` has no default size.
- Decision 2: recommendation moved from ignore to reject, because the ZFS tools reject `-s` on datasets.
- Decision 9: new framing. The `democratic-csi:` default prefix is a parity default. The OpenZFS-loyal option is an endpoint-owned module prefix, with adoption as an explicit opt-in.
- Decision 10: adoption is now expressed as `zfs set`/`inherit` operations, not a tool.
- DeleteVolume with snapshots is not a separate numbered item. It sits in decision 9's compatibility scope in the first version, and it should become its own decision. Recommended OpenZFS-loyal default: refuse with FAILED_PRECONDITION while snapshots exist, matching plain `zfs destroy` (no `-r`). Parity alternative: `destroy -r`, which takes foreign snapshots. Deferred destroy (`-d`) for snapshots with clones is already the OpenZFS-native path for snapshot deletion (`internal/endpoint/plan.go`, PlanDeleteSnapshot).
- Detached snapshots (democratic-csi's `detachedSnapshotsDatasetParentName`) were previously listed as a compatibility item. OpenZFS model: a snapshot belongs to its dataset, and moving it out requires a clone. Recommended OpenZFS-loyal default: do not build detached snapshots. Parity alternative: build them as a democratic-csi adoption feature only.
