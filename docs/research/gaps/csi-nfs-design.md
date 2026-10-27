# wave6: NFS volumes in the CSI front end (design, then attack)

HEAD 0c884df, read-only. Governing decision: ADR 0004. Export state lives only in `sharenfs`, and OpenZFS is the source of truth. Static export at create comes first. Per-node publish may follow behind a flag. The node side is csi-driver-nfs. OpenZFS 2.4 only.

**Built (wave13).** Static export at create, §1's removal of `nfs_clients`, and the CSI NFS mode are implemented. Where the build differs from the text below, a note marked "As built" says how and why. §13 Q1 (root ownership) is built as option (a), and §4's local mountpoint pin (wave14). Not built yet: §10's `reshare` action and the D1-D6 reports, and §12.

Sources checked (upstream repositories, by repo-relative path):
- openzfs `cmd/zfs/zfs_main.c:763-810,1340-1365` (create, then mount and share)
- openzfs `module/zfs/zfs_ioctl.c:3980-4100` (create ioctl)
- openzfs `lib/libzfs/libzfs_dataset.c:1177-1250` (sharenfs validation) and `:1590-1665` (`set -u`)
- openzfs `lib/libzfs/libzfs_mount.c:630-660` (unshare before unmount)
- openzfs `lib/libzfs/os/linux/libzfs_share_nfs.c` (whole file)
- nfs-utils `support/export/client.c:268-300,776-800`, `support/nfs/exports.c:167-176`, `support/export/cache.c:3314-3360`
- csi-driver-nfs `pkg/nfs/nodeserver.go:60-230`
- democratic-csi `controller-zfs-generic/index.js:116-150,623-655`

## 0. Facts the design rests on (verified in source)

| # | Fact | Where |
|---|---|---|
| A | `zfs create`/`clone -o sharenfs=X` is **not atomic with the share**. The ioctl runs `dmu_objset_create`, then a *separate* `zfs_set_prop_nvlist`, and destroys the dataset if the second step fails. Userland then mounts, shares and runs `exportfs -ra`. Mount or share failure is "non-fatal": exit 0. | zfs_ioctl.c:4066-4095; zfs_main.c:1356-1363, 783-806 |
| B | An invalid sharenfs is refused at create or set time (`sa_validate_shareopts`). An *unsafe* one is not: `rw=` (empty), `rw=a::b` and `rw=a:` give an empty host. libshare writes `mp (sec=sys,rw,…)`, and nfs-utils reads an empty host as `*`. **World export.** Bare `rw`/`ro` also means `*`, and `on` means `rw,crossmnt` to `*`. | libzfs_share_nfs.c:136-206, 300-345, 394-410; exports.c:171-176 |
| C | nfs-utils classes a client string containing `/` as MCL_SUBNETWORK. It keeps the string verbatim, matches it by `strcasecmp` and never resolves it. A bare IP or name is MCL_FQDN: resolved by DNS, merged by address, written under its canonical name. | client.c:276-291, 790-795 |
| D | When several exports of one path match a client, mountd takes the **first** one in type order (FQDN before subnet) and file order. It does not take the most specific ("arbitrarily choosing options from first"). | cache.c:3321-3357 |
| E | `zfs destroy` of a mounted fs runs unshare, `exportfs -ra`, unmount, destroy, in that order. If the unmount fails it **re-shares** and fails. | libzfs_mount.c:645-656; zfs_main.c:1532-1535 |
| F | `zfs set -u` skips the changelist postfix, so it never mounts or shares. A plain set on an unmounted dataset with `canmount=on` mounts it and shares it. | zfs-set.8:77-79; libzfs_dataset.c:1659-1662 |
| G | csi-driver-nfs reads only `server`, `share` (both required), `subdir`, `mountoptions`, `mountpermissions`, `uid`, `gid` from volume_context. It ignores the other keys. A `chmod`/`chown` by node root is squashed under `root_squash`. | nodeserver.go:85-131, 186-224 |

## 1. Decision on `<prefix>:nfs_clients`

**It must go.** It is a client list of our own: `sharenfs` "is computed from it" (export.go:19-23). That is a second source of truth, which ADR 0004 rules out by name ("no client list … of our own"). Storing it in ZFS does not make it OpenZFS's state, because it is the endpoint's opinion of what sharenfs *should* be. The disagreement case shows it. An admin edits sharenfs. With `nfs_clients` present, the endpoint would either overwrite the edit on the next publish or call the value foreign (`foreign()` = sharenfs local ≠ off with `nfs_clients` unset). Both put our list above OpenZFS.

`nfs_clients` did three jobs. Each has a replacement that keeps no state:
- **Ownership marker.** Replaced by a strict *canonical grammar* on sharenfs (§2).
- **ro/rw per client.** Already present in sharenfs (`rw=`/`ro=`).
- **Canonical comparison.** Done by parsing sharenfs to a set and comparing sets.

Migration: delete `encodeClients`/`decodeClients`, `NFSClientsSet` and the table column. A leftover `nfs_clients` property is reported by reconcile and is otherwise ignored.

As built: the property is never read for export state or written. The table keeps one boolean (`NFSClientsSet`), so that reconcile can report a leftover property in `skipped`. Rolling back is safe only to the bridge commit, which takes a canonical sharenfs as ours without `nfs_clients` and still writes the list. An older build would compute sharenfs from a stale list and revoke clients. A legacy sharenfs written from it with bare IPs or hostnames is now non-canonical, so it is FOREIGN (§2). The admin fixes it once with `zfs set sharenfs=rw=<ip>/32…`. The project is pre-release, so expect no such volumes.

## 2. Canonical grammar and ownership (the core rule)

```
canonical := "off" | rw [ "," ro ] | ro
rw        := "rw=" list          ro := "ro=" list
list      := cidr { ":" cidr }   (non-empty, sorted, no duplicates)
cidr      := dotted-quad IPv4 "/" N, host bits zero, N >= minPrefix, always with "/N" (also /32)
and: no two cidrs in the whole value overlap (contain one another), whatever their mode
```

Ownership on a managed filesystem is all-or-nothing, decided only by the value itself:
- **MANAGED.** sharenfs is set locally and parses as canonical. The endpoint may rewrite the whole value, *whoever typed it*. An admin edit that stays in the grammar is accepted as truth.
- **FOREIGN.** sharenfs is set locally and is anything else: `on`, options such as `no_root_squash`, `sec=`, hostnames, `@`, `*`, empty hosts, overlaps, or a prefix below the floor. The endpoint never writes it. It only reads it and reports it.
- **INHERITED.** sharenfs is not set locally. It is treated as unexported and reported, and the next endpoint write sets it locally.

There is no per-entry ownership. It would need state. Every entry in a MANAGED value is "ours". Every entry in a FOREIGN value is the admin's.

Why `/N` is always written, even `/32`: by Fact C, every client we write is matched as a verbatim string with no DNS. That one rule removes adv4 F1–F3 and F5's hex aliases (§9).

`minPrefix` is an endpoint flag (`-nfs-min-prefix`, default 16). It is reported in GetCapabilities.

As built, the floor is **not** part of the ownership grammar. It applies to new writes, so Publish and CreateVolume refuse a network below it. Ownership accepts any `/N` from 1 to 32. With the floor in the grammar, raising `-nfs-min-prefix` would turn existing exports FOREIGN. They could then never be rewritten or unpublished, which breaks the flag's promise that raising it never strands an export. `0.0.0.0/0` stays out because it fails the round trip (`/0` is refused by the parser), and reconcile is the place to report `below-floor`. `nfs_min_prefix` is not in GetCapabilities yet.

## 3. StorageClass parameters

| key | mode | meaning |
|---|---|---|
| `nfs.clients` | static (required there) | Comma-separated IPv4 CIDRs, e.g. `10.20.0.0/24,10.20.1.0/24`. Each CIDR is validated as in §2 and refused if non-canonical: host bits set, no `/N`, overlap, or below the floor. The value is never silently masked. In per-node mode it is refused (InvalidArgument). |
| `nfs.server` | both, optional | The address put into volume_context `server`. It must be one of the endpoint's advertised `nfs_servers`, otherwise InvalidArgument. The default is the first advertised entry. |

- The endpoint's flag `-nfs-server` is repeatable: IPv4 or DNS name, advertised in `GetCapabilitiesResponse.nfs_servers`. The endpoint is the one place that knows its storage addresses. On a multi-homed host the SC chooses among them (§9.5).
- The export mode is not a parameter. It is `ro=` when *every* requested access mode is a `*_READER_ONLY` mode, and `rw=` otherwise.
- With the NFS node side, the controller refuses `block` access type in CreateVolume (InvalidArgument). csi-driver-nfs cannot publish a zvol, and ADR 0004 leaves block without a node story.

## 4. CreateVolume

Proto (additive):
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

The raw value, the state enum and `nfs_min_prefix` wait until a front end needs them.

`props.go` keeps refusing `sharenfs` in the generic `properties` map. The value comes only from `nfs_export`, in canonical form.

Plan for a new filesystem:
1. `zfs create -o sharenfs=<canonical> -o <prefix>:nfs_server=<server> -o mountpoint=<parentMP>/<id> -o <prefix>:managed=true … <ds>`. (As built, wave14: every filesystem volume is pinned, NFS or not, in the same `-o` block as F12's `setuid`/`exec`/`devices=off`; the parent's mountpoint is read once per create. A parent whose mountpoint is `none` or `legacy` pins nothing. A volume made before the pin is never pinned by the endpoint, since `zfs set mountpoint` remounts it under its users: Reconcile reports it in `skipped`.) A clone uses `zfs clone -o …` the same way. Setting it at create only replaces today's `sharenfs=off`. Pinning `mountpoint` locally keeps volume_context `share` valid if the parent moves (csi-node-seam §5.3). It is a native property, so it is not state of our own.
2. Verify the export:
   - `/proc/fs/nfsd/threads` > 0;
   - etab at `EvalSymlinks(mountpoint)` lists exactly the canonical clients with their modes, compared as strings.
   - Otherwise reshare once (`zfs set sharenfs=<same>`, valid per adv4 "re-setting re-shares") and re-check.
   - Still wrong: **Unavailable**, with the commit marker *not* set.
3. Set the commit marker (`provision_success`). The marker therefore means "created **and served**".

"Atomic with create?" Only partly (Fact A). The property is set inside the create ioctl, so no committed volume can exist without its sharenfs. But:
- the ioctl itself has a window (objset created, props not yet set);
- the userland share step can fail silently.

Both are covered by crash points C1–C3 in §8. Do not count on `-o sharenfs` for atomicity. It saves one `zfs set` and one `exportfs -ra`.

volume_context (§5) also needs dataset root ownership (§11 Q1). Under `root_squash` a fresh root-owned 0755 dataset is writable by nobody.

## 5. volume_context

`server`, `share` (the volume's mountpoint), plus the existing `dataset`, `kind`, `mountpoint`.

`server` is the address recorded at create in the local `<prefix>:nfs_server`: `nfs.server`, or the first advertised one. It is never recomputed (wave13 Q1), because CSI requires a volume to "always report the same volume_context" and a PV's attributes are immutable. Both keys are present only for a volume created with an export. A volume created before that reports neither, which is what it was created with. An inherited or received `nfs_server` belongs to no volume. It is ignored and reported (reconcile).

Nothing else:
- No `subdir`.
- No `mountoptions`. Mount options go in SC `mountOptions` and reach the node as `mount_flags`.
- No `uid`/`gid`/`mountpermissions`, which root_squash breaks (Fact G).
- `publish_context` stays empty, in both modes.

## 6. Idempotency when parameters differ on retry

The CSI name maps to a volume id. Then:
- **Uncommitted** (no marker). Nobody holds its volume_context, so the endpoint converges sharenfs to the request (step 1 becomes a `zfs set`). This covers a crash and an SC recreated with other values.
- **Committed, MANAGED.** Compare the *parsed sets* of clients plus mode, so order is irrelevant.
  - Equal: OK. Return the volume. It does not re-verify etab, because reconcile owns that (§10).
  - Different: **ALREADY_EXISTS** ("exported rw to X, requested Y"). This holds even when the difference is an admin's in-grammar edit, so the message names the current value. It matches the existing rule for `properties`.
- **Committed, FOREIGN or INHERITED.** **ALREADY_EXISTS** with the raw value. This covers adopted volumes, which are not re-created through CreateVolume anyway.
- **`nfs.server`** (retracted: "not volume state" contradicted the spec's immutable volume_context; wave13 Q1).
  - Uncommitted: converges to the request, like sharenfs.
  - Committed: a different explicit server is **ALREADY_EXISTS**, naming the recorded one. An omitted server returns the recorded one, even after `-nfs-server` was reordered.
  - A recorded server that is no longer advertised is still returned. The PVs hold it, and reconcile reports it (D8).

## 7. DeleteVolume, access modes, Validate

**Delete ordering.** No explicit unshare. The existing precondition checks (CSI snapshots, dependent clones) run first. A refusal must leave the volume *unchanged*, including its export, per the proto contract. Then `zfs destroy` performs unshare → exportfs → unmount → destroy itself (Fact E):
- If the unmount is busy, zfs re-shares and fails, so the volume is unchanged and the call returns ABORTED/retry.
- A FOREIGN value is destroyed the same way. democratic-csi's extra `zfs inherit sharenfs` before destroy is not needed.
- No etab check after delete. A stale etab line for a gone path disappears at the next `exportfs -ra`, and reconcile reports it (D6).

**Access modes (static mode).** FILESYSTEM supports all five modes.
- MULTI_NODE_* works because the export is a CIDR.
- SINGLE_NODE_* is *advisory*: with `attachRequired: false` Kubernetes does not fence across nodes. This is the same as democratic-csi and truenas-csi. Document it.
- `readonly`: the server-side mode comes from §3. csi-driver-nfs adds `ro` to the mount from NodePublish `readonly` (Fact G).

**ValidateVolumeCapabilities.** Confirmed iff all of these hold:
- the kind is FILESYSTEM;
- access is `mount` with `fs_type` "" or "nfs";
- if `nfs_state` is MANAGED and the value has only `ro=` clients, then no writer mode is requested;
- the parameters parse in the current mode;
- if volume_context is given, each key it names equals the volume's, compared per key: `share` is the current mountpoint, and `server` is the **recorded** one, not merely an advertised one. Kubernetes' own keys (`storage.kubernetes.io/…`) are ignored.

As built, Validate also refuses a volume created without an export, and one whose recorded server is no longer advertised.

A mismatch returns `Message` (not an error). This is the only drift a front end can see without its own state.

**CSIDriver**: `attachRequired: false`, `fsGroupPolicy: None` (a kubelet chown as squashed root fails).

## 8. Crash points (attacked)

| | Crash or failure after… | Visible as | Recovery |
|---|---|---|---|
| C1 | objset created, props not set (inside the ioctl, Fact A) | Dataset without the managed marker. sharenfs is **inherited from the parent** | Fixed by requiring the parent's effective sharenfs = `off`, checked at endpoint start (refuse to start) and in reconcile (D5). The orphan then exports nothing, not even after `zfs share -a` at boot. The "unmanaged leftover makes the create ALREADY_EXISTS forever" problem predates this design and is out of scope. |
| C2 | props set, before mount or share | managed, uncommitted, not in etab | Retry: uncommitted → `zfs set sharenfs=<want>`. On canmount=on this also mounts (Fact F). Then verify and commit. Boot: zfs-mount and zfs-share serve it, which is harmless (empty). Reconcile GC destroys it if it is abandoned. |
| C3 | shared, but exportfs failed or nfsd is down (exit 0) | uncommitted, etab missing it, or threads = 0 | CreateVolume returns Unavailable, and the retry reshares. Provisioning waits for NFS. That is honest, because the volume would be unusable. |
| C4 | verified, before the marker | uncommitted, served | Retry: sets equal, etab OK → commit. |
| C5 | marker set, before the response | committed | Retry: compatible → OK. |
| C6 | destroy: unshared, before unmount or destroy | Dataset present, sharenfs set, not in etab | Delete retry. If boot comes first, `zfs share -a` re-serves it until the retry. Acceptable. |
| C7 | per-node set, before verify | value contains the host | The Publish retry sees the host present, verifies, OK. |
| C8 | admin edit between our read and our write (no CAS in `zfs set`) | The admin's edit is lost and replaced by our canonical value | Shrunk by re-reading inside the volume lock just before the write. It cannot be closed. Documented rule: an admin who wants a value kept writes a FOREIGN one, which the endpoint never writes. Locks are in-process, so this assumes one endpoint per pool. |

## 9. Attacks

### 9.1 Hostile or careless admin edits

| Edit | Classified as | Effect |
|---|---|---|
| `rw=10.0.0.0/24:` / `rw=a::b` / `rw=` | FOREIGN | **Exports to everyone** (Fact B). `zfs set` accepts these. Reconcile reports `world-export`, the highest severity. The endpoint never writes them, and our writer cannot produce an empty host because a non-empty sorted list is joined with `:`. |
| `on`, `rw`, `*`, `0.0.0.0/0`, `/8` with floor 16 | FOREIGN | world-export or below-floor drift. This is also how democratic-csi's `sharenfs=on` volumes look. |
| `rw=10.0.0.0/24,ro=10.0.0.5/32` | FOREIGN | Overlap. By Fact D the `/32` gets **rw** (first line wins), so ro is not enforced. That is why the grammar bans overlap. |
| `rw=X,sec=krb5,ro=Y` | FOREIGN | `sec` applies to later hosts (libshare :127-128). Options are never ours. |
| `rw=10.9.0.0/16` (in grammar) | MANAGED | Accepted as truth. A CreateVolume retry would be ALREADY_EXISTS. In per-node mode it counts as a published client, and unpublish-all removes it. |
| `zfs inherit sharenfs` | INHERITED | Parent must be off, so access is revoked. Reported. |
| `off` | MANAGED | A decision, not drift. Nodes lose access. Static mode cannot tell this from intent without state. ValidateVolumeCapabilities does not refuse it. |
| `/etc/exports` line for our path, or an ancestor with `crossmnt` | n/a | An extra etab client (D3-extra) or ancestor crossmnt (D5). An FQDN line outranks our subnet lines (Fact D). **Refused, not just reported** (updated wave13). Publish is FAILED_PRECONDITION while anything serves the path beyond the volume's own list, and it is checked before any change, so a refused Publish changes nothing. Unpublish is FAILED_PRECONDITION while the client is still served. CreateVolume does not commit a volume served beyond its export. |
| An admin's `rw=*`, or `rw=<ours>:*` | FOREIGN | Never rewritten (wave13). Publish is FAILED_PRECONDITION. Unpublish of a client the `*` still serves is FAILED_PRECONDITION. Before, it was rewritten to our list. |

### 9.2 CIDR overlap
- **Within one value:** banned (Fact D), as above.
- **Across volumes or SCs:** different paths, no interaction.
- **Per-node `/32` inside a MANAGED static CIDR:** impossible, because the modes are exclusive (§12). If an admin creates it, it is an overlap and the value is FOREIGN, so Publish returns FAILED_PRECONDITION.
- **A CIDR too wide:** the floor. Policy *between* tenants (one SC's CIDR covering another tenant's nodes) is the SC author's job. SCs are cluster-admin objects.

### 9.3 The eight adv4 export findings

| | Disposition |
|---|---|
| F1 hostname revocation on `exportfs -ra` | **Gone.** No hostnames. `/N` makes every client MCL_SUBNETWORK, with no DNS (Fact C). The residual (an nfs-server restart, a lost etab) is repaired by reconcile `reshare` (D3). |
| F2 address-merge | **Gone for our entries.** Subnet clients are matched by string only and never merged with FQDN clients. etab shows our exact string. A foreign FQDN line for the same path shows up as D3-extra. |
| F3 prefix match | **Gone.** `Lookup` becomes exact string equality on the path's client set. The `HasPrefix` rule is deleted. |
| F4 nfsd not running | Create, Publish and Unpublish require `/proc/fs/nfsd/threads` > 0 before OK (Unavailable otherwise). Reconcile reports D4. Unpublish while nfsd is down: the property change is still made, then Unavailable. The next retry sees no host and no server, which is OK: nothing serves it. |
| F5 `0.0.0.0/0`, hex aliases | Floor (`-nfs-min-prefix`). `netip.ParsePrefix` accepts only dotted decimal with no leading zeros, so `0x…` forms cannot parse. |
| F6 realpath | Compare etab with `filepath.EvalSymlinks(mountpoint)`. The pinned local mountpoint keeps `share` stable. |
| F7 length | Limit 8191 (ZFS_MAXPROPLEN-1), on sharenfs only, now that `nfs_clients` is gone. A `/32` costs up to 19 bytes, so about 430 nodes per volume in per-node mode. Beyond that: ResourceExhausted. |
| F8 unpublish mounts an unmounted volume | When `!mounted`, write with `zfs set -u` (Fact F), which updates the property without mounting or sharing. Publish on an unmounted volume: canmount=on lets zfs mount and serve it (correct). noauto or off is FAILED_PRECONDITION with an accurate message. |

### 9.4 Crash points
See §8. Each one leaves a state that a retry or reconcile can classify from sharenfs, etab and the marker alone.

### 9.5 Multi-homed servers
- **Which address clients use.** `-nfs-server` lists the storage addresses (a VIP for HA, or a DNS name so a PV survives a readdress). `nfs.server` picks one per SC and is validated against the list. At start the endpoint warns, without refusing, when an IPv4 entry is not a local interface address, because a floating VIP may be absent.
- **Exports are not per server address.** nfsd answers on every interface. sharenfs authorizes only by *client source address*. So "volume X only via storage network A" cannot be expressed in sharenfs. That needs a firewall or nfs.conf `host=`, which is deployment, not this design.
- **The client's source address must fall in the CIDR.** On a multi-homed node, routing picks the source address toward `server`. If it is outside `nfs.clients`, NodePublish fails with PermissionDenied: closed, and visible. In per-node mode this is sharper, because node_id must equal *that* source address, not `status.hostIP` (csi-node-seam §1a). That is a deployment requirement on csi-driver-nfs's `--nodeid`, stated in the per-node design. Static mode avoids it by covering the storage subnet.
- **etab verification is by path, not by server address.** It is unaffected.

## 10. Reconcile: reporting drift

Add `ReconcileResponse.drift = 3 { target, kind, detail }` and one action, `reshare`. Reconcile reads every managed filesystem's sharenfs, the etab and the nfsd threads once. It never writes a value of its own choosing. The only write is `reshare`: re-setting a **MANAGED** value unchanged, on a mounted volume, while nfsd runs, when etab misses one of the value's clients or has the wrong mode for one. That is re-applying OpenZFS's own state (adv4: re-set re-shares). All other drift is report-only.

| kind | condition |
|---|---|
| D1 `inherited` | sharenfs not local on a managed filesystem |
| D2 `foreign` / `world-export` / `below-floor` | non-canonical value. world-export = empty host, `*`, bare rw/ro, `on`, or /0 |
| D3 `not-served` (→ reshare) / `extra-served` | etab for realpath(mountpoint) ≠ the parsed MANAGED value |
| D4 `nfsd-down` | threads = 0 (global) |
| D5 `parent-shared` / `ancestor-crossmnt` | the parent's effective sharenfs ≠ off; or an etab export of an ancestor path has crossmnt |
| D6 `orphan-export` | etab path under the parent's mountpoint matching no volume |
| D7 `legacy-nfs_clients` | `<prefix>:nfs_clients` still set |
| D8 `server-unadvertised` | a committed volume's recorded `<prefix>:nfs_server` is not in `-nfs-server` (its PVs still mount it) |
| D9 `server-elsewhere` | `<prefix>:nfs_server` inherited or received: ignored, not the volume's |

As built (wave13): D7, D8 and D9 are reported in the existing `ReconcileResponse.skipped`, one entry per volume, so no `drift` field was added. D1-D6 and `reshare` are not built.

"Drift" means OpenZFS disagrees with *itself* (sharenfs against etab), or with policy (grammar, floor, parent). It never means OpenZFS disagrees with a stored desire, because none is stored. A static volume an admin switched to `off` is not drift (§9.1).

## 11. Adopting democratic-csi NFS volumes

- **Endpoint side: nothing new.** The volumes already read as managed through the prefix. Their sharenfs (`on`, or `rw=@net,no_root_squash`) is FOREIGN, so it is served untouched and reported (D2, usually `world-export` for `on`). Delete works (Fact E).
- **CSI side.** `PV.spec.csi.driver` is immutable, so adoption means re-creating the PVs under our driver name as static PVs:
  - `volumeHandle` = our id;
  - `volumeAttributes.server/share` from democratic-csi's `democratic-csi:csi_share_volume_context` JSON (server = its `shareHost`, share = mountpoint).
  - That is a one-off tool, not endpoint code.
- **Switching to MANAGED.** It is opt-in: the admin sets a canonical value, e.g. `zfs set sharenfs=rw=10.20.0.0/24 …`. The endpoint never converts on its own, because that would revoke clients.
- **`no_root_squash`.** Adopted volumes often depend on it. Converting them is blocked by Q1 below.

## 12. Per-node publish (later, behind a flag, no new state)

- **Flag.** Controller and endpoint flag `nfs-mode=per-node`. It is driver-wide because `attachRequired` is per CSIDriver. Create then sets `sharenfs=off` and refuses `nfs.clients`. Static and per-node never mix on one driver, so every entry in a MANAGED value is a published node.
- **Publish** (`node_id` must parse as a single IPv4, written `/32`; ADR 0004 "never a CIDR" refers to *meaning*, `/32` is only the spelling):
  - mode = `readonly || *_READER_ONLY`;
  - FOREIGN or INHERITED value → FAILED_PRECONDITION;
  - same host, same mode → verify only;
  - same host, other mode → ALREADY_EXISTS;
  - SINGLE_NODE_* while another host is present → FAILED_PRECONDITION. This is enforceable now, because sharenfs *is* the client list.
  - Otherwise add the host, write, and verify.
- **Unpublish.**
  - host absent and not served → OK;
  - FOREIGN while served → FAILED_PRECONDITION (the admin overrode it);
  - otherwise remove the host and write. Use `-u` when unmounted. The last host goes to `off`, set locally.
  - **Empty node_id = unpublish-all**: a MANAGED value goes to `off`. This removes admin in-grammar additions too, which is the documented price of having no per-entry ownership.
- **Proto.** `UnpublishFileExportRequest.all = 3`. The `client` field narrows to single IPv4 (CIDRs only via CreateVolume).

## 13. Owner questions (only these need the owner)

- **Q1. Root squash vs. dataset root ownership.** The canonical grammar emits no options, so `root_squash` applies. A new dataset root is `root:root 0755`, so **no pod can write a new volume**, and csi-driver-nfs's chown or chmod is squashed (Fact G). Two options:
  - (a) Keep root_squash. Add `CreateVolumeRequest` root `uid`/`gid`/`mode` (SC `nfs.uid/gid/mode`), applied by the endpoint to the mountpoint before the commit marker. This is file metadata, not export state.
  - (b) Admit exactly one option, `no_root_squash`, into the canonical grammar via an SC flag.
  
  Recommendation: (a). Adopted democratic-csi volumes stay FOREIGN either way.

  As built (wave14): (a). `NFSExport` carries optional `uid`, `gid` and `mode` (SC `nfs.uid`, `nfs.gid`, `nfs.mode`). The endpoint sets them on the mounted root through a descriptor opened without following any symlink and checked against the mount table, in the resume before verification and commit, on every pass (idempotent, so crash-safe without a record). Unset leaves an attribute as the create made it: root:root 0755, or a clone's source's. A committed volume is not compared on them.

Decided here, not owner questions:
- Pinning mountpoint locally, because volume_context requires a stable `share`.
- The `/16` default floor, which is configurable.
- Dropping hostnames, which ADR 0004 allows ("validated as a single host").
