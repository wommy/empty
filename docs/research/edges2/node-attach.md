# NODE ATTACH survey

Pinned commits: gobrick 81d24769, goiscsi 13d890fe, gonvme 6de21896, csi-driver-iscsi 6c1fd20e, go-iscsi-helper 35fee3c3, mayastor 1e796a58. All six are Apache-2.0 (LICENSE files). Some searches were still indexing, so claims rest on direct reads and greps.

## EDGES

Boundary: CSI node code to kernel initiator (iscsiadm, nvme-cli, sysfs, /dev, multipathd).
Crosses: portal, target IQN or NQN, LUN or NSID, WWN/NAA or nguid, CHAP secret (as argv or DB value), device node, multipath map name.

## Findings

| Connector | Device resolution | Single-LUN removal | Multipath flush | CHAP storage |
|---|---|---|---|---|
| dell/gobrick | iSCSI/FC: WWN via multipath name and `"3"+wwn` SCSI lookup (`base.go:94-135`). NVMe: `nvme list` + `id-ns`, match nguid and NSID (`nvme.go:283-298`) | Yes. `cleanConnection` finds HCTL per session+LUN and deletes only those SCSI devices (`iscsi.go:284-317`). `DisconnectVolume` never logs out (`iscsi.go:236-245`) | `cleanDevices` calls `cleanMultipathDevice`, which retries `multipath -f` (10 retries, 120s timeout, `base.go:39-41,307-367`). Without force, flush failure aborts before SCSI delete (`base.go:315-322`) | Map passed to `CreateOrUpdateNode` (`iscsi.go:680-686`). goiscsi writes `-o update -n node.session.auth.password -v <secret>` in argv (`goiscsi_iscsi.go:560-580`) |
| dell/goiscsi | Not a resolver | No. `performLogout` runs `iscsiadm -m node -T -p --logout` for the whole node (`goiscsi_iscsi.go:306-331`) | None | Same argv path via `SetCHAPCredentials` (`goiscsi_iscsi.go:523-529`) |
| dell/gonvme | `nvme list -o json` plus `nvme id-ns` (`gonvme_tcp_fc.go:799-830,962`) | No. `nvme disconnect -n NQN` drops the whole controller (`gonvme_tcp_fc.go:775-796`) | None | None found (grep for chap/dhchap/secret empty). Connect uses argv (`:590`) |
| csi-driver-iscsi | `/dev/disk/by-path/ip-<portal>-iscsi-<iqn>-lun-<n>` (`pkg/iscsilib/iscsi.go:341-343`) | No. `Disconnect` calls `Logout(iqn, portal)` (`iscsi.go:396-410`). This is the baseline pattern | None found. Only checks one multipath child (`iscsi.go:215-226`) | `-n node.session.auth.password -v <secret>` in iscsiadm argv (`pkg/iscsilib/iscsiadm.go:127-160`) |
| longhorn/go-iscsi-helper | `iscsiadm -m session -P 3` matched on IP/target/LUN (`iscsi/initiator.go:244-254`) | No. `StopInitiator` logs out the whole target (`iscsidev/iscsi.go:247,272-311`; `iscsi/initiator.go:154-166`) | None found (no multipath in source) | None found |
| openebs/mayastor | NVMe-oF URI `nvmf://host:port/nqn` with optional hostnqn and uuid (`io-engine/src/bdev/nvmx/uri.rs:158-201`). SPDK user-space bdevs per namespace, no /dev node | Unverified | Unverified | Unverified |

`nvme connect` and `disconnect` in mayastor appear only in test helpers (`io-engine-tests/src/nvme.rs:85-150`). The kernel attach path is not verified.

## SEAMS

1. Device identity, from gobrick: match on WWN (SCSI `3`+NAA) or nguid+NSID, and never on /dev names. Adopt this for resolution.
2. Detach, from gobrick: per-LUN removal (`cleanConnection`) followed by `cleanDevices` (flush, then delete, fail closed without force). Adopt the sequence, not the library.
3. Session control, from goiscsi: `iscsiLib`-style Go interface (`CreateOrUpdateNode`, `PerformLogin`, `PerformLogout`) so the endpoint can swap the implementation. Keep logout out of unstage.
4. CHAP: the argv write is the flaw in goiscsi and csi-driver-iscsi. Put the iscsiadm call behind the interface above, so a fixed implementation can replace it. Unverified whether iscsiadm accepts a non-argv secret input.

## GAPS

- No surveyed connector combines per-LUN detach with secrets kept off argv. gobrick has the first, and both gobrick and goiscsi have the second problem.
- csi-driver-iscsi, goiscsi, gonvme and longhorn all log out the whole session or controller on unstage. The endpoint can add value by detaching single LUNs and only logging out when no LUN remains.
- Nothing in this set persists detach state across a crash. The endpoint's name-keyed idempotency fills that gap.
- mayastor and FC/NVMe multipath details are unverified.
