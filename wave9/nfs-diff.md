# Wave 9: differential fuzzing of the NFS export chain

Target: the `wommy/empty` repo at HEAD 1603dad. Work copy: `wave9/nfs-diff/` (base commit "base"). Fix diff: `wave9/nfs-diff.diff`, made with `git diff HEAD` in the copy, untracked files included. Toolchain: go1.27.3.

The chain under test:

```
endpoint request
  -> ShareNFS (internal/endpoint/export.go)
  -> zfs set sharenfs
  -> OpenZFS libshare
  -> /etc/exports.d/zfs.exports
  -> nfs-utils exportfs -ra
  -> /var/lib/nfs/etab
  -> ParseEtab / serves / Lookup (our verifier)
  -> mountd lookup_export
  -> kernel
```

The simulator (`internal/zfs/zfstest/sim.go`) stands in for everything from `zfs set` to etab.

## 1. Reference models (`internal/nfsref`, new package, pure Go)

The models are derived line by line from the C code. Each function names its source file and line numbers.

| File | Models | Source |
|---|---|---|
| `libshare.go` | `ValidateSharenfs`, `foreach_nfs_shareopt`, the host splitter (IPv6 brackets), `get_linux_shareopts` (valid-key list, `anon`, `root_mapping`, `nosub`), `get_linux_hostspec` (drops `@`), `nfs_escape_mountpoint`, `ExportsLines`, `ShareFile` (`nfs_toggle_share` and `nfs_copy_entries`) | OpenZFS a128b8a: `lib/libzfs/os/linux/libzfs_share_nfs.c`, `libzfs_share_nfs.c`, `libzfs_share.c`, `libzfs_dataset.c` |
| `exports.go` | xio tokenizer (`xgettok`: quotes, `\ooo`, `#` comments), `getexportent` (default options, anonymous clients, `(` meaning `*`), `parseopts` (all options, `sec=` flavors, per-flavor flags, `squash_uids`, `xprtsec`), `putexportent` (the etab spelling) | nfs-utils 3.1.1: `support/nfs/exports.c`, `xio.c` |
| `exportfs.go` | `client_gettype` (FQDN, SUBNET, WILDCARD, NETGROUP, ANON, GSS), strict `inet_pton` / `inet_aton`, subnet parsing (`/024`, `/+24`, dotted masks, a bare `/` meaning /0), `client_lookup` (merging by address or string), `export_lookup` and `export_add` (first entry wins on duplicates), the 1021-bucket hash order, `xtab_write`, `ExportfsRA` (exports plus `exports.d`, sorted with strverscmp), `LoadEtab` | `support/export/{client,export,xtab,hostname}.c`, `utils/exportfs/exportfs.c` |
| `access.go` | mountd `lookup_export` (class order, then list order; V4ROOT; crossmnt with the longer path winning), `path_matches` and `is_subdirectory`, the `mountpoint` check, the per-flavor sys mode (`Access`, `Exposure`) | `support/export/cache.c` |

Name resolution, realpath and mountpoint checks are passed in as functions. Behaviour that comes from libc or the kernel is marked "ambiguity" in the code.

### Validating the models against a real exportfs

I built nfs-utils 3.1.1 from source, with its paths pointed at a scratch root. `TestExportfsMatchesReal` (`internal/nfsref/exportfs_real_test.go`) generates random input for three files and compares the real etab with the model's byte for byte:

- `/etc/exports`
- `exports.d/00-early.exports`
- `zfs.exports`, produced by running random sharenfs values through `ShareFile`

The generator covers bad prefixes, inet_aton spellings, quotes, `#`, `-defaults`, `sec=` lists, `squash_uids`, `xprtsec` and line continuations.

Results:

- **4000 cases, 3947 byte-identical, 0 mismatches.**
- The other 53 cases crashed exportfs, and the test skips them. Section 4 has the crash.

To run it:

```
NFSREF_EXPORTFS=<built exportfs> NFSREF_ROOT=<root with exports, exports.d/, state/> \
  go test -run TestExportfsMatchesReal ./internal/nfsref
```

Without `export_features` (no nfsd), the test uses the old secinfo flag mask.

## 2. Differential fuzz targets (`internal/endpoint/nfs_diff_test.go`)

All four targets are in the CI fuzz matrix. Each ran for 45-60s on the fixed copy with no failures. Their seeds fail on HEAD as expected.

- **`FuzzNFSRoundTrip`** checks that client list, `ShareNFS`, libshare, exportfs, etab, `ParseEtab` and mountd give each client exactly the requested mode and nothing else. It also checks that the simulator's `zfs.exports` and etab match the models byte for byte.
- **`FuzzNFSClassifier`** feeds raw sharenfs values, both local and inherited, through ownership classification (MANAGED/FOREIGN/INHERITED). It then checks Publish and Unpublish against the models: a success must never leave more access than requested, or leave the host still served.
- **`FuzzNFSVerifier`** tests verifier soundness. It adds random foreign etab sources (`/etc/exports`, other `exports.d` files, other datasets with the same mountpoint, crossmnt ancestors). Whenever `serves()` or `Lookup` says the client gets exactly mode M, the mountd model must agree for every sampled address.
- **`FuzzNFSEtabParse`** checks that `ParseEtab` agrees with `LoadEtab` on etab-shaped lines.

## 3. Divergences by verdict

### Encoder bugs: none

`ShareNFS` output always passes `ValidateSharenfs`, is canonical, and grants exactly what was asked. `ParseClient` only lets digits, `.` and `/` through.

### Verifier bugs, all fail-open (exporting more than requested), fixed

**D1 (high): crossmnt ancestor.**

- **Problem:** a parent dataset with `sharenfs=on` (which expands to `rw,crossmnt`), or any crossmnt export of an ancestor path, exports every child volume to the parent's clients. mountd (`path_matches`) serves the child through the parent's line.
  - Pinning the child's sharenfs locally does not help.
  - HEAD's Publish returned success. `TestFileExportAdoptedVolumes` asserted that success.
  - The HEAD simulator hid the problem by always writing `nocrossmnt`.
- **Fix:**
  - `PlanPublishExport` now refuses with FailedPrecondition when a crossmnt ancestor exists (`crossmntAncestors`).
  - `PublishFileExport` re-checks `exposure()` after applying.
  - The test was updated: a parent with "on" is refused; a parent with `rw=@10.0.0.0/8` is accepted.

**D2: `serves()` ignored precedence and options.**

- **Problem:** mountd takes the first match in class order: an FQDN line beats any subnet, and an earlier overlapping subnet beats a later one. Foreign lines can therefore decide what our client gets. The sources include `/etc/exports`, another dataset sharing the mountpoint, and subnet spellings that Go cannot parse (`/024`, `/+24`, dotted masks).
- HEAD also ignored options that widen access:
  - a per-flavor mode such as `ro,sec=sys,rw`, where the kernel enforces the sys flavor's rw;
  - `no_root_squash`, `insecure`, `anonuid=0`, `crossmnt`, and flavors other than sys.
- **Fix:**
  - `ParseEtab` classifies options with an allow-list (`etabOptions`). Any other option marks the line `broad`.
  - `serves()` requires our line to be the first subnet line that overlaps the client, with no FQDN, wildcard or netgroup line that may match, and the line must not be broad.

**D3: Unpublish reported success while the host was still served.**

- **Problem:** the client could still be served after Unpublish returned success:
  - by an admin edit of a managed sharenfs;
  - by an inherited world export (`rw=`, `rw=#`, or a value with a space) when the stored value is `none`.
- **Fix:** `covers()` runs after the Lookup check. Lookup only matched our exact /N line.
- Datasets with foreign sharenfs are deliberately exempt.

### Simulator infidelities (HEAD), all fixed

The simulator now delegates to `nfsref`, using `ShareFile` and `ExportfsRA`. HEAD's simulator diverged from the models in 14 catalogued cases:

| Case | What HEAD's simulator got wrong |
|---|---|
| Empty host (`rw=`, `rw=a:`) | Shown as `""` instead of `*` |
| `@` prefix | Not stripped |
| Duplicate entries | Kept, instead of first-wins |
| crossmnt | Never shown |
| `mountpoint` option | Missing |
| Invalid sharenfs | Accepted |
| sec null, bad option value, quote | File truncation not modelled |
| Space in a value | Host split and default ro options not modelled |
| `#` in a value | World ro export not modelled |
| `/33`, unresolvable names | Not dropped |
| IPv6 brackets | Not parsed |
| etab order | Not ordered by client class |

Two more fidelity changes:

- Re-setting `off` on a dataset with no lines does not run exportfs, as in real libzfs (`changelist_postfix` only unshares datasets that are shared).
- `SetNFSDown` now means "exportfs unavailable".

### Reference-model ambiguities (need a real host)

These are checked by the replay test and recorded in its report:

- glibc printing `(null)` for `sec` with no value;
- the canonical name getaddrinfo returns for numeric hosts (stays numeric on the build host);
- `strtoul`/`errno` edge cases;
- the `export_features` flag mask;
- `same_path` handle/inode fallback (bind mounts);
- reexport;
- wildcard and netgroup resolution;
- DNS latency for `.invalid` names.

## 4. Other findings

- **exportfs 3.1.1 crash.** `exportfs -ra` aborts with "free(): double free" when a line has default options containing `squash_uids` and two or more clients.
  - Smallest reproducer: `/a -squash_uids=1-5 10.0.0.1 10.0.0.2(rw)`.
  - When this happens, etab and the kernel are not updated at all.
  - sharenfs cannot produce this input (`squash_uids` is not a libshare key, and libshare never writes `-defaults`). Only an admin's `/etc/exports` can.
  - With the D1-D3 fixes, our endpoint then fails closed: Publish sees no line, and Unpublish sees the host still covered.
  - Possibly an upstream bug.
- **One bad line stops the rest of the file.** A malformed line from one dataset (for example `sec` with no value, a bad option value, or a quote) stops exportfs reading the rest of `zfs.exports`. That drops other datasets' exports: a denial of service across datasets that fails closed.
- `rw=#` gives a world read-only export.
- Tab, `\v` and space pass `zfs set` validation.

## 5. Client grammar decision

**Recommendation: one grammar on the wire and in stored state, always a masked IPv4 CIDR with an explicit /N.** The API may keep accepting a bare address as input sugar, normalised to `/32` at `ParseClient`. That is what the code already does, so the PublishFileExport finding is input leniency only. A bare address never reaches sharenfs, `zfs.exports`, etab or the stored client list:

- `ParseClient` returns `p.String()`, which gives `10.0.0.5/32`;
- `decodeClients` refuses a stored host whose spelling is not canonical;
- `Lookup` only matches subnet lines.

If the CSI design wants to be strict, make the request field require /N too, so that one spelling exists end to end. Rejecting bare input is a usability choice, not a kernel-state difference.

Evidence. Each item was checked with the models and the real exportfs build, and is pinned by `internal/nfsref/grammar_test.go`.

| Question | Bare `10.0.0.5` | `10.0.0.5/32` |
|---|---|---|
| nfs-utils class | FQDN (`client_gettype`: no `/`, not a wildcard, netgroup or GSS) | SUBNET |
| DNS at exportfs time | Goes through getaddrinfo. Numeric hosts give no network lookups under strace, but libc decides the result (canonical-name ambiguity) | Never resolved (`inet_pton` only) |
| Merging | Merged with any name client that resolves to the same address. `/a node-a(ro)` then `/a 10.0.0.5(rw)` gives etab `node-a(ro)`: our entry is renamed, its mode is lost, and exportfs prints "incompatible duplicated export entries ... IGNORED" | Never merged. Deduplicated only against the identical string. `/a node-a(ro)` then `/a 10.0.0.5/32(rw)` keeps both lines |
| etab spelling | The canonical name of the first client with that address. Can be a name we never wrote | Kept verbatim: `10.0.0.5/32` |
| mountd precedence | FQDN class comes first, so it beats every subnet whatever the file order: `/32(ro) bare(rw)` gives rw | Loses to any FQDN line for the same address. Our fixed `serves()` detects this and refuses to confirm |
| Endpoint readback | `ParseEtab`/`Lookup` cannot tie a renamed or merged line to the request | Exact string and network match |

How the two forms diverge in kernel state: when the same address appears in both forms, both lines go into etab. Then:

- the bare (FQDN) line decides the mode;
- a foreign hostname line can absorb the bare line;
- with `/32` only, our line is never rewritten, and any foreign line that would override it is visible in etab and caught by the verifier.

The real exportfs agreed with the model in every case:

- `localhost(ro) 127.0.0.1(rw)` merges into one line;
- `localhost(ro) 127.0.0.1/32(rw)` gives two lines.

## 6. Real-nfsd replay (`internal/endpoint/nfs_replay_test.go`)

`TestNFSReplay` runs a corpus of 42 sharenfs values on a real dataset, with a parent dataset for the inherited and crossmnt cases.

The corpus covers:

- off, on, bare values;
- /32, /16, mixes, duplicates and overlaps;
- `/024`;
- bare addresses and `@`;
- empty hosts, `*`, unresolvable names;
- crossmnt, `no_root_squash`, `root_mapping`, `sec`, `sec` with no value;
- `#`, space, quote;
- bad `anonuid`, sync, bogus options, `""`, `[::1]`, `mountpoint=`;
- parent set to "on" and parent set to a subnet.

For each value it diffs four things against the models and the simulator:

- the `zfs set` result;
- the dataset's `zfs.exports` lines;
- the dataset's etab lines;
- the mode the mountd model computes from the real etab, along with whether our verifier's verdict is sound.

**Gates:**

- It runs only when `ZFS_TEST_POOL` is set and `/proc/fs/nfsd/threads` is greater than 0.
- If `ZFS_REQUIRE_REAL` is set but there is no pool, the test fails.

**Outcomes:**

- These count as hard failures:
  - divergences for canonical (endpoint-written) values;
  - unsound verifier verdicts.
- Other divergences are recorded as ambiguities, and fail the test only when `NFS_REPLAY_STRICT` is set.
- There is a 30s budget, and the test aims for 25s.
- The report goes to `$NFS_REPLAY_REPORT`, default `./nfs-replay.log`.

`TestNFSReplayOnSim` runs the same corpus against the simulator: 0 failures in 45ms.

### CI hook

- `ci/real-zfs-vm.sh` already runs every `endpoint.test` test as root in the VM, with `ZFS_TEST_POOL=zfsep-test ZFS_REQUIRE_REAL=1` and nfs-server enabled. TestNFSReplay therefore runs there with no new step.
- After the scp, the script adds `nfs-replay.log` to `$GITHUB_STEP_SUMMARY` in a `<details>` block. If the log is missing, the script emits `::error` and fails.
- `.github/workflows/ci.yml` adds `FuzzNFSRoundTrip`, `FuzzNFSClassifier`, `FuzzNFSVerifier` and `FuzzNFSEtabParse` to the fuzz matrix.

## 7. How to run

```
cd wave9/nfs-diff
GOTOOLCHAIN=go1.27.3 go test ./...
go test -run '^$' -fuzz FuzzNFSVerifier -fuzztime 60s ./internal/endpoint
sudo ZFS_TEST_POOL=<pool> go test -run TestNFSReplay -v ./internal/endpoint
```

`go test ./...` passes, including `go vet` and gofmt.
