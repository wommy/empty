# adv4: adversarial review of NFS file export (HEAD bdf89d0)

Scope: internal/endpoint/export.go, endpoint.go:541-648, table.go, zfstest/sim.go NFS part.
Sources used:
- OpenZFS at `scratchpad/repos/openzfs` (a128b8a). libshare now lives in `lib/libzfs/libzfs_share_nfs.c` and `lib/libzfs/os/linux/libzfs_share_nfs.c`; the path `lib/libshare/os/linux/nfs.c` does not exist in this tree.
- nfs-utils 3.1.1, cloned to `scratchpad/repos/nfs-utils` (github.com/linux-nfs/nfs-utils, 5090e0b).
- Proof tests: `scratchpad/adv4/export-copy/internal/endpoint/adv4_export_test.go`. Run `GOTOOLCHAIN=go1.27.3 go test ./internal/endpoint/ -run Adv4 -v`. All pass, and each passing test shows the bug. The tests wrap the simulator's etab in `nfsUtils`, an emulation of nfs-utils. It drops unresolvable names and writes an FQDN client under the first canonical name seen for its address.

Not re-reported: ownership (received ≠ owned), error classification, CSI items (wave3/adv3).

## What checks out (no finding)

- **sharenfs → exports line.** `ShareNFS` emits `rw=a:b,ro=c`. libshare splits options on `,` and hosts on `:` (os/linux/libzfs_share_nfs.c:73-109, 140-207). It writes one line per host: `<mp> <host>(sec=sys,<rw|ro>,no_subtree_check,mountpoint)` (:388-392, :425-427, sec default "sys" :228). crossmnt is added only for `on` (:61-62), so the endpoint never exposes child datasets. root_squash, sync and secure come from nfs-utils defaults. CIDR is passed through verbatim (`get_linux_hostspec` only strips `@`, :244-252). The always-added `mountpoint` option means an unmounted path is not served from the parent fs.
- **Injection.** The allowlist `[A-Za-z0-9./-]` plus per-label checks (export.go:49-50, 68-99) keep `, : = ( @ * [` whitespace and unicode out. `[` would trigger libshare's IPv6-literal parsing (:154). Nothing reaches the property unescaped.
- **etab format.** `path<TAB>host(ro|rw,...)` with the first option being the mode (nfs-utils support/nfs/exports.c:287-289). Paths are written `\ooo`-escaped (:267-275). ParseEtab handles both.
- **Concurrency on one host.** libzfs rewrites zfs.exports under `flock(zfs.exports.lock)` with tmp+rename (libzfs_share_nfs.c:31-56, 244-277). `exportfs` serializes itself with `lockf` (exportfs.c:93-114, 238-239) and writes etab via tmp+rename (xtab.c:92-117). Every `exportfs -ra` rebuilds from all files, so concurrent publishes on different datasets, or two endpoints, converge. The per-volume lock is enough for the endpoint's own read-modify-write of `nfs_clients`.
- **Re-setting an unchanged value re-shares.** `zfs set` has no same-value short-circuit. `changelist_postfix` always re-shares a mounted dataset with sharenfs≠off (libzfs_changelist.c postfix: "always re-share even if currently shared"), so `reshare()` is valid. The NFS prefix step does not unshare first (only mountpoint/sharesmb, changelist_prefix), so there is no access gap.
- **Reboot.** State lives in properties. zfs-share.service runs `zfs share -a` After=zfs-mount, Before=nfs-server (etc/systemd/system/zfs-share.service.in). nfs-server's ExecStartPre `exportfs -r` reads /etc/exports.d (nfs-utils systemd/nfs-server.service:25). The one exception for hostnames is F1.

## Findings (ranked)

### F1. HIGH: hostname clients are silently revoked by any later `exportfs -ra` that cannot resolve them, and nothing restores them
- **Scenario.** Volume Y is published to `node-a`. Later, any sharenfs change on any dataset runs `exportfs -ra`: another volume's publish, `zfs share -a` at boot, or an nfs-server reload. If DNS or NSS fails for `node-a` at that moment, nfs-utils skips the entry. `client_lookup`: `host_addrinfo` fails, it logs "Failed to resolve", and returns NULL (support/export/client.c:277-281; export_create :233-236). `-r` rebuilds etab only from the exports files (exportfs.c:264-266: `if (!f_reexport) xtab_export_read()`, then `xtab_export_write` + `cache_flush`), so node-a's export on Y is removed and the kernel cache is flushed. The node loses access. The endpoint never re-checks: verification happens only inside Publish and Unpublish, and reconcile.go has no export logic. CSI does not re-call ControllerPublish for an attached volume, so the loss persists until the next exportfs -ra that happens to resolve.
- **Where.** export.go:53-99 (hostnames accepted), endpoint.go:554-603 (verify only at publish time).
- **Fix.** Accept only IPv4 addresses and CIDRs. CSI node IDs or the CO can supply IPs, so no DNS sits in the export path. If hostnames must stay, add a periodic export reconcile (etab vs stored lists, then `reshare`).

### F2. MEDIUM-HIGH: etab names a client by the first name nfs-utils saw for its address, so exact-string verification fails forever (and one host can appear under two modes)
- **Scenario A (proved: `TestAdv4_AddressDedupPublishNeverVerifies`).** The storage host's /etc/exports has `/srv/backup node-a(rw)`, with node-a = 10.0.0.5. Then `PublishFileExport(pvc, "10.0.0.5")` happens. exportfs reads /etc/exports first (exportfs.c:240-242). For "10.0.0.5", `client_lookup` finds the existing node-a client by address (`client_check`, client.c:284-286). etab writes `m_client->m_hostname` (xtab.c:109-112): `/…/pvc\tnode-a.example(rw…)`. `Etab.Lookup(path,"10.0.0.5")` misses. The result is Unavailable on every retry, each retry re-running `zfs set` and `exportfs -ra`. The same happens across endpoint volumes when different volumes name one node differently, and in the hostname-after-IP direction.
- **Scenario B (`TestAdv4_TwoSpellingsBypassModeConflict`).** On one volume, `node-a` rw then `10.0.0.5` ro (the same machine) is not AlreadyExists, because the strings differ (export.go:333-336). nfs-utils keeps the first line and ignores the duplicate (`export_read` → `warn_duplicated_exports`, export.c:86-100, 123-129). The ro request is never honored, and its Publish is Unavailable forever. The hex spellings in F5 (`10.0.0.0x5`) are a third spelling of the same address.
- **Narrow fail-open.** If exportfs did not run (the case verification exists to catch), Unpublish of an aliased client checks the wrong name and returns success while the old entry is still served.
- **Where.** export.go:217-226 (`Lookup` compares strings), endpoint.go:597-601, 640-645.
- **Fix.** Compare by address. For an IP or CIDR client, resolve etab hostnames (net.DefaultResolver honors /etc/hosts and NSS like exportfs) and match when the address sets intersect. Treat two clients resolving to the same address as one client for the AlreadyExists check.

### F3. MEDIUM: `Lookup`'s prefix rule matches other clients (`node-a` ⇒ `node-a.evil.example`)
- **Unpublish livelock (proved: `TestAdv4_PrefixMatchUnpublishLivelock`).** A volume has clients `node-a` and `node-a.evil.example`. Unpublish(node-a) removes node-a correctly, but the verification `Lookup(path,"node-a")` matches the remaining `node-a.evil.example` line (export.go:221). The result is Unavailable on every retry, so ControllerUnpublish never completes and the detach hangs. The pre-check at export.go:368 is also wrong: Unpublish of a host that was never published becomes a reshare plus Unavailable.
- **Publish false success (proved: `TestAdv4_PrefixMatchPublishFalseSuccess`).** `node-a` is unresolvable on the storage host, so exportfs drops it (client.c:279-281). `node-a.evil.example` is served ro. Publish(node-a, ro) returns success with nothing serving node-a, because the mode is taken from the other client's line.
- **Also.** If `node-a` canonicalizes through a CNAME to a name that does not start with `node-a.`, Publish is Unavailable forever.
- **Where.** export.go:217-226.
- **Fix.** Drop the prefix rule. Either use IP-only clients (F1), or resolve the requested name with `AI_CANONNAME` semantics and compare against that exact canonical name or address set (F2).

### F4. MEDIUM: "etab serves it" does not mean the NFS server is running; the simulator models the wrong failure
- **Scenario.** `systemctl stop nfs-server` runs `exportfs -au` and empties etab (nfs-server.service:28). Any later `zfs set sharenfs` runs `exportfs -ra`, which writes etab without checking for nfsd (exportfs.c:260-271: `xtab_export_write(); cache_flush();`). Publish then returns success, so ControllerPublish succeeds, while nothing listens. The error text "check that the NFS server is running" (endpoint.go:599-600, 643-644) promises a check the code does not make.
- **Simulator.** `SetNFSDown` freezes the old table (sim.go:731-753). That is neither "server stopped" (table emptied, then repopulated) nor the common real partial failure (one unresolvable or duplicate host dropped while the rest apply). TestFileExportVerifiesKernelTable therefore proves a failure path that real hosts rarely take and misses the ones above. The sim also writes hosts verbatim with no canonicalization, dedup or realpath (sim.go:759-779), so F1–F3 and F6 cannot appear in the sim.
- **Fix.** In Publish, also require `/proc/fs/nfsd/threads` > 0, which the contract harness already checks (export_contract_test.go:22-25). Report Unavailable otherwise. Give the sim a "stopped" mode (etab cleared, then rebuilt on set) and a per-host drop or alias hook.

### F5. MEDIUM: client policy fails open. `*` is refused, but `0.0.0.0/0` and inet_aton aliases are accepted (proved: `TestAdv4_ClientPolicyGaps`)
- `ParseClient("0.0.0.0/0")` gives sharenfs `rw=0.0.0.0/0`, which exports to every IPv4 host. The code comment (export.go:54-57) says wildcards are refused because "*" would export to every host. `0.0.0.0/1` and `128.0.0.0/1` together do the same.
- `10.0.0.0x5`, `0xa000005` and `0x7f.0x1` pass the "partial address" guard (export.go:95-98 checks only the decimal last label). glibc resolves them as addresses: `getaddrinfo` gives 10.0.0.5 / 10.0.0.5 / 127.0.0.1 with canonname equal to the input (verified on glibc 2.39). The guard's intent (no ambiguous numeric forms) is bypassed, which feeds F2-B.
- **Fix.** Refuse prefixes shorter than a floor (at least refuse /0, ideally make the floor configurable, e.g. /16). Refuse a hostname whose labels are all `[0-9a-fx]` numeric forms (any label matching `^(0x[0-9a-f]+|[0-9]+)$` in every position), or simply refuse hostnames (F1).

### F6. LOW-MEDIUM: etab paths are realpath'd, but `Lookup` uses the mountpoint property
- nfs-utils canonicalizes each exports path with `nfsd_realpath` (support/nfs/exports.c:204-208), and etab stores that. If the mountpoint crosses a symlink (e.g. `/srv` → `/data`), `row.Mount` ≠ etab path. Every Publish is Unavailable forever. Unpublish verification is blind (always "not served"), so it reports success even when exportfs failed to remove the entry: a fail-open in exactly the case verification exists for.
- **Where.** export.go:345, endpoint.go:597, 640.
- **Fix.** `filepath.EvalSymlinks(row.Mount)` before the lookup, or refuse to publish when it differs from `row.Mount`.

### F7. LOW: the 4095-byte limit is wrong, and the stored list is longer than sharenfs
- `ZFS_MAXPROPLEN` is `ZAP_MAXVALUELEN` = 8192 (include/libzfs.h:47, include/sys/fs/zfs.h:76), and libzfs refuses `strlen >= ZFS_MAXPROPLEN` (lib/libzfs/libzfs_util.c:1831). The real sharenfs maximum is 8191, not 4095 (export.go:44-46). The design doc's "about 4 KiB" (adv-file-export-design.md:66) was never re-checked.
- Today this only refuses large lists early. If someone fixes the constant without care, `nfs_clients` (`host=rw,` costs 4 bytes per host versus 1 in sharenfs) passes 8191 first, for example about 585 IPv4 hosts. The atomic `zfs set` then fails with E2BIG. The user-property limit is enforced in kernel zfs_ioctl.c (not in this sparse checkout), so it is unverified here.
- **Fix.** Set the limit to 8191 and check `len(encodeClients(want))` too (exportOps, export.go:290-293).

### F8. LOW: Unpublish can mount an unmounted volume, and Publish's refusal reason is inaccurate
- `changelist_postfix` mounts a dataset that is not mounted when sharenfs≠off and canmount=on (libzfs_changelist.c postfix lines 78-87 of the function), then shares it. Unpublish of one of two clients on an unmounted volume (export.go:356-385 has no mount check) sets `sharenfs=rw=<other>`. That mounts the filesystem, which an operator may have deliberately unmounted, and starts serving it.
- Conversely, Publish refuses unmounted volumes with "zfs would store the export and serve nothing" (export.go:326-328). For canmount=on, zfs would in fact mount and serve.
- The sim cannot show either: `mounted()` is derived from canmount/mountpoint (sim.go:716-726), so it has no "canmount=on but unmounted" state, and it treats noauto as never mounted.
- **Fix.** When `!row.Mounted`, have Unpublish change only `nfs_clients` and set `sharenfs` only when it is going to "off". Alternatively, document the mount side effect. Reword the Publish error.

## Not substantiated / dropped
- exports.d race between concurrent publishes: serialized by libzfs flock plus exportfs lockf (see above).
- Option injection through `=` or `:` in client specs: blocked by the allowlist.
- The sharenfs string disagreeing with libshare's defaults: it matches. No crossmnt; sec=sys and root_squash are the defaults.
