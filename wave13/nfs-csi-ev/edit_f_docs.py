import sys
root = sys.argv[1]


def edit(path, pairs):
    p = root + '/' + path
    s = open(p).read()
    for old, new in pairs:
        if s.count(old) != 1:
            raise SystemExit('anchor in %s found %d times: %s' % (path, s.count(old), old[:80]))
        s = s.replace(old, new, 1)
    open(p, 'w').write(s)


edit('docs/research/gaps/csi-nfs-design.md', [
('''HEAD 0c884df, read-only. Governing decision: ADR 0004. Export state lives only in `sharenfs`, and OpenZFS is the source of truth. Static export at create comes first. Per-node publish may follow behind a flag. The node side is csi-driver-nfs. OpenZFS 2.4 only.
''', '''HEAD 0c884df, read-only. Governing decision: ADR 0004. Export state lives only in `sharenfs`, and OpenZFS is the source of truth. Static export at create comes first. Per-node publish may follow behind a flag. The node side is csi-driver-nfs. OpenZFS 2.4 only.

**Built (wave13).** Static export at create, §1's removal of `nfs_clients`, and the CSI NFS mode are implemented. Where the build differs from the text below, a note marked "As built" says how and why. Not built yet: §4's local mountpoint pin, §13 Q1 (root ownership), §10's `reshare` action and the D1-D6 reports, and §12.
'''),
# §1
('''Migration: delete `encodeClients`/`decodeClients`, `NFSClientsSet` and the table column. A leftover `nfs_clients` property is reported by reconcile and is otherwise ignored.''',
 '''Migration: delete `encodeClients`/`decodeClients`, `NFSClientsSet` and the table column. A leftover `nfs_clients` property is reported by reconcile and is otherwise ignored.

As built: the property is never read for export state or written. The table keeps one boolean (`NFSClientsSet`), so that reconcile can report a leftover property in `skipped`. Rolling back is safe only to the bridge commit, which takes a canonical sharenfs as ours without `nfs_clients` and still writes the list. An older build would compute sharenfs from a stale list and revoke clients.'''),
# §2 floor
('''`minPrefix` is an endpoint flag (`-nfs-min-prefix`, default 16). It is reported in GetCapabilities.''',
 '''`minPrefix` is an endpoint flag (`-nfs-min-prefix`, default 16). It is reported in GetCapabilities.

As built, the floor is **not** part of the ownership grammar. It applies to new writes, so Publish and CreateVolume refuse a network below it. Ownership accepts any `/N` from 1 to 32. With the floor in the grammar, raising `-nfs-min-prefix` would turn existing exports FOREIGN. They could then never be rewritten or unpublished, which breaks the flag's promise that raising it never strands an export. `0.0.0.0/0` stays out because it fails the round trip (`/0` is refused by the parser), and reconcile is the place to report `below-floor`. `nfs_min_prefix` is not in GetCapabilities yet.'''),
# §4 proto
('''Proto (additive):
- `CreateVolumeRequest.nfs_export = 10 { repeated string clients; bool read_only; }`
- `Volume.sharenfs = 10` (raw local value, "" if inherited)
- `Volume.nfs_state = 11` (OFF | MANAGED | FOREIGN | INHERITED)
- `Volume.nfs_clients = 12` (parsed, MANAGED only)
- `GetCapabilitiesResponse.nfs_servers = 4`, `nfs_min_prefix = 5`''',
 '''Proto (additive):
- `CreateVolumeRequest.nfs_export = 10 { repeated string clients; bool read_only; }`
- `Volume.sharenfs = 10` (raw local value, "" if inherited)
- `Volume.nfs_state = 11` (OFF | MANAGED | FOREIGN | INHERITED)
- `Volume.nfs_clients = 12` (parsed, MANAGED only)
- `GetCapabilitiesResponse.nfs_servers = 4`, `nfs_min_prefix = 5`

As built (wave13), the proto change is smaller:
- one message, `NFSExport { repeated string clients = 1; bool read_only = 2; string server = 3; }`;
- `CreateVolumeRequest.nfs_export = 10`;
- `Volume.nfs = 10`, which holds the recorded server and the local sharenfs when it is canonical;
- `GetCapabilitiesResponse.nfs_servers = 4`.

The raw value, the state enum and `nfs_min_prefix` wait until a front end needs them.'''),
('''1. `zfs create -o sharenfs=<canonical> -o mountpoint=<parentMP>/<id> -o <prefix>:managed=true … <ds>`.''',
 '''1. `zfs create -o sharenfs=<canonical> -o <prefix>:nfs_server=<server> -o mountpoint=<parentMP>/<id> -o <prefix>:managed=true … <ds>`. (As built: the mountpoint is not pinned yet.)'''),
# §5
('''`server` (from `nfs.server`), `share` (the volume's mountpoint as read after create), plus the existing `dataset`, `kind`, `mountpoint`.''',
 '''`server`, `share` (the volume's mountpoint), plus the existing `dataset`, `kind`, `mountpoint`.

`server` is the address recorded at create in the local `<prefix>:nfs_server`: `nfs.server`, or the first advertised one. It is never recomputed (wave13 Q1), because CSI requires a volume to "always report the same volume_context" and a PV's attributes are immutable. Both keys are present only for a volume created with an export. A volume created before that reports neither, which is what it was created with. An inherited or received `nfs_server` belongs to no volume. It is ignored and reported (reconcile).'''),
# §6
('''- **`nfs.server` differs.** Not compared. It is not volume state, and the response carries the new value.''',
 '''- **`nfs.server`** (retracted: "not volume state" contradicted the spec's immutable volume_context; wave13 Q1).
  - Uncommitted: converges to the request, like sharenfs.
  - Committed: a different explicit server is **ALREADY_EXISTS**, naming the recorded one. An omitted server returns the recorded one, even after `-nfs-server` was reordered.
  - A recorded server that is no longer advertised is still returned. The PVs hold it, and reconcile reports it (D8).'''),
# §7
('''- if volume_context is given, `share` equals the current mountpoint and `server` is advertised.''',
 '''- if volume_context is given, each key it names equals the volume's, compared per key: `share` is the current mountpoint, and `server` is the **recorded** one, not merely an advertised one. Kubernetes' own keys (`storage.kubernetes.io/…`) are ignored.

As built, Validate also refuses a volume created without an export, and one whose recorded server is no longer advertised.'''),
# §9.1 last row
('''| `/etc/exports` line for our path, or an ancestor with `crossmnt` | n/a | An extra etab client (D3-extra) or ancestor crossmnt (D5). An FQDN line outranks our subnet lines (Fact D). Reported only. |''',
 '''| `/etc/exports` line for our path, or an ancestor with `crossmnt` | n/a | An extra etab client (D3-extra) or ancestor crossmnt (D5). An FQDN line outranks our subnet lines (Fact D). **Refused, not just reported** (updated wave13). Publish is FAILED_PRECONDITION while anything serves the path beyond the volume's own list, and it is checked before any change, so a refused Publish changes nothing. Unpublish is FAILED_PRECONDITION while the client is still served. CreateVolume does not commit a volume served beyond its export. |
| An admin's `rw=*`, or `rw=<ours>:*` | FOREIGN | Never rewritten (wave13). Publish is FAILED_PRECONDITION. Unpublish of a client the `*` still serves is FAILED_PRECONDITION. Before, it was rewritten to our list. |'''),
# §10
('''| D7 `legacy-nfs_clients` | `<prefix>:nfs_clients` still set |''',
 '''| D7 `legacy-nfs_clients` | `<prefix>:nfs_clients` still set |
| D8 `server-unadvertised` | a committed volume's recorded `<prefix>:nfs_server` is not in `-nfs-server` (its PVs still mount it) |
| D9 `server-elsewhere` | `<prefix>:nfs_server` inherited or received: ignored, not the volume's |

As built (wave13): D7, D8 and D9 are reported in the existing `ReconcileResponse.skipped`, one entry per volume, so no `drift` field was added. D1-D6 and `reshare` are not built.'''),
])

edit('docs/adr/0004-support-floor-and-node-side.md', [
('''- Block (zvol) volumes still need a node story; csi-driver-nfs covers only NFS.''',
 '''- Block (zvol) volumes still need a node story; csi-driver-nfs covers only NFS.

## Implementation notes (2026-10-27, branch `nfs-csi`)

These notes record how the decision was built. They do not change it.

- **`nfs_clients` is gone from the export path.**
  - sharenfs alone is read and written.
  - Reconcile reports a leftover property in `skipped`. It is never cleared, so it stays on disk as evidence: `zfs inherit` clears it.
  - Every list the property could hold round-trips through sharenfs (fuzzed), so no state is lost.
  - Roll back only as far as the bridge commit "a canonical local sharenfs is ours without nfs_clients". An older build computes sharenfs from a stale list and revokes clients.
- **The floor is not in the ownership grammar**, a deviation from "at or above the configured floor" above.
  - The floor bounds new writes: Publish and CreateVolume.
  - Ownership accepts any `/N` from 1 to 32. Otherwise, raising `-nfs-min-prefix` would turn existing exports foreign, and they could never be rewritten or unpublished.
  - Reconcile is the place to report below-floor values.
- **A foreign value is never written, even when it still serves the client.**
  - An admin's `rw=*` over our list is not rewritten.
  - Unpublish is FAILED_PRECONDITION while a foreign value still serves the host.
  - A refused Publish changes nothing.
- **The NFS server address is addressing metadata, not export state.**
  - CSI requires it to be fixed per volume, and OpenZFS has no place for it. So `<prefix>:nfs_server` is recorded once, in the same `zfs create`/`clone -o` as sharenfs, and read local-only (wave13 Q1).
  - Who may mount is still sharenfs alone.
- **NFS mode** is on iff the endpoint advertises `-nfs-server`. In that mode every CSI CreateVolume needs `nfs.clients` (wave13 Q2).
- **Not built yet:**
  - root ownership ("Root squash stays on");
  - the local mountpoint pin;
  - the reconcile drift reports other than the leftover list and the server.'''),
])

edit('README.md', [
('''desired clients in a local `<prefix>:nfs_clients` property with `sharenfs` computed from it, each Publish verified against `/var/lib/nfs/etab` (at the mountpoint's real path) and running nfsd threads; root only (`docs/research/gaps/file-export.md` and its reviews). ADR 0004 since decided that export state lives only in `sharenfs`: `nfs_clients` is to be removed in a follow-up and reconcile is to report a leftover one (neither is done yet). SMB not started |''',
 '''export state in `sharenfs` alone (ADR 0004: a local value in the endpoint's grammar is the endpoint's, anything else is never written; a leftover `<prefix>:nfs_clients` is ignored and reported by Reconcile), each Publish verified against `/var/lib/nfs/etab` (at the mountpoint's real path) and running nfsd threads, a Publish refused for an export beyond the volume's list changing nothing; static export at create (`CreateVolumeRequest.nfs_export`, servers advertised with `-nfs-server`, the chosen one recorded per volume in `<prefix>:nfs_server`, committed only once served); root only (`docs/research/gaps/file-export.md`, `docs/research/gaps/csi-nfs-design.md`). SMB not started |'''),
('''| CSI front end (`internal/csi`, `cmd/zfs-csi`) | controller + identity done; csi-sanity Controller/Identity groups run on the simulator and real ZFS. Node side not started, so block access is opt-in (`-enable-block`, default off) |''',
 '''| CSI front end (`internal/csi`, `cmd/zfs-csi`) | controller + identity done; csi-sanity Controller/Identity groups run on the simulator and real ZFS. NFS mode when the endpoint advertises `-nfs-server`: StorageClass `nfs.clients` (required) and `nfs.server`, all five access modes, `server`/`share` in volume_context for csi-driver-nfs (ADR 0004). No node plugin of our own, so block access is opt-in (`-enable-block`, default off) and refused in NFS mode |'''),
])
