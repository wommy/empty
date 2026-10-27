# Impossible states in the ZFS endpoint: inventory, redesign, prototype

**Question.** Which field combinations can the core types represent that mean nothing? Which of them are reachable? What Go shapes make them unrepresentable, or at least unreachable past one parse boundary, while keeping the DOD pipeline: lock → one `zfs get -Hp` scan → immutable indexed Table of Rows → pure planner → Plan of fat-struct Ops → apply → re-read?

**Pins.**
- **B** is HEAD `9f1790d`. It already contains the wave-3 fixes merge (`2246735`: A–F21, including F20 `apply` refusing an unknown kind and F21 table coverage), `32bc4ec` (decode fails on unparsable numbers and impossible zvol sizes), and ADR 0004. All `file:line` references are to B unless marked **P**.
  - The brief named `bdf89d0`. HEAD moved twice during this work, so I rebased onto the newest base.
  - A first version of the prototype, built on `3107a48` (the fixes branch before its merge), is kept at `wave5/types-copy-3107a48` and `wave5/proto-on-3107a48.diff`.
- **P** is the prototype at `wave5/types-copy` (= B + `wave5/prototype.diff`).

**Not re-reported** (done or assigned elsewhere): ownership predicates, received ≠ local, op-aware error codes, the props allowlist, prefix validation, foreign-snapshot refusal, the apply default case, coverage flags, the 32bc4ec number checks, and the ADR 0004 `nfs_clients` removal. I design on top of them, and point out only where one of them is incomplete as a *type*.

**Evidence files** (all in `wave5/`):
- `reach-before.txt`, `reach-staged-vs-proto.txt`, `reach-drift.txt`, `staged-partial-delete.txt`: logs of the reachability probes.
- `bench-layout.txt`, `bench-decode.txt`, `bench-scan-strict.txt`: benchmark output.
- `final-test.txt`: test results.

---

## 0. Answer in brief

Three changes carry most of the value. All three are built and tested in P:

1. **One ownership enum, `Own`, decided once in decode** (`Foreign`, `VolUncommitted`, `VolCommitted`, `SnapCSI`, `SnapCloneSource`; the zero value is `Foreign`). It replaces the `Managed`/`Committed` booleans.
   - B still has **four** definitions of "ours":
     - `OwnedVolume`/`OwnedSnapshot` (table.go:66-82);
     - an inline copy of `OwnedVolume` in the resume branch (plan.go:165-171);
     - name-only `internalCloneSource` in DeleteVolume (plan.go:395-423);
     - `prefix && !Managed` in reconcile (reconcile.go:205-210).
   - They disagree on a row ZFS can hold. A probe confirms it (§1 R7).
2. **`FullTable` in planner signatures**, with `scanAll`/`scanVolume` replacing `scan(ctx, "")`.
   - F21 made whole-parent coverage a runtime check, and got it wrong at one of the three planners that need it: `PlanDeleteVolume` checks only `mustCover(id)` (plan.go:382), but its `ClonesOf` loop (plan.go:400) needs the whole parent. The probe plans `destroy -r` from a one-volume table while a clone exists elsewhere.
   - With the type, the three runtime checks become compile errors.
3. **The decode boundary rejects rows zfs never prints** (type ∉ {filesystem, volume, snapshot}; '@' in the name ⇔ snapshot) and leaves zero the columns a row's type does not have.
   - This kills partially forged phantom rows. B lists a forged `a@ghost` as a *filesystem volume*.
   - It sits on top of 32bc4ec, which handles numbers.

A 40-line AST test makes "only table.go builds Rows, FullTables and whole-coverage Tables" hold *inside* the package, where unexported fields protect nothing. A compile-fail test runs the real compiler over the removed states with `go build -overlay`.

Cost:
- Strict decode: +4.7 ms per 10k rows (best of 8 runs) and **0 extra allocations**, about 1% of a 300–450 ms scan.
- Layout, at most 8 ms per 10k rows, is noise next to parsing. Decide layout on types, not speed.

---

## 1. Inventory

**Status key:**
- **R**: reachable, with where from.
- **G**: guarded, with the check at `file:line`.
- **U**: representable but not reached today.

**Fix key:**
- **P1**: the `Own` enum.
- **P2**: the decode boundary.
- **P3**: `FullTable`, the scan split and `sizing`.
- **Rn**: a recommendation in §2.

### Row (table.go:16-47)

| # | Representable combination | Why meaningless | Status | Fix |
|---|---|---|---|---|
| R1 | Snapshot row with volume columns: `VolumeName`, `SourceType`, `SourceID`, `NFSClients*`, `Local`, `Refquota`, `Refres`, `Mount`, `Origin` | A snapshot has no quota, export or provision source | **R** from ZFS state: zfs accepts user properties on snapshots, and decode copies every locally set user property onto every row (`own`, table.go:241-247). Native ones print "-" and read as 0. Harmless today because every reader gates on Type first (export.go:266 via `OwnedVolume`). | P2 zeroes them (P table.go `switch typ`) |
| R2 | Volume row with snapshot columns: `SnapshotName`, `SnapshotSource`, `Deferred` | Only snapshots have them | **R** for the user properties (same path as R1). `defer_destroy` prints "-". G by Type in `OwnedSnapshot` (table.go:78-82). | P2 |
| R3 | Filesystem with `Volsize`/`BlockSz`; zvol with `Refquota` | Not that kind's columns | **U** from zfs (prints "-"). **Fabricated in production**: plan.go:239 builds `Row{Type: kindType(r.Kind), Volsize: …}`, and plan.go:269-273 copies a real row and zeroes its quota to stand in for a clone. | P3 `sizing{Volsize, Refquota, Refres}` replaces the fake rows |
| R4 | `Type` ∉ {filesystem, volume, snapshot} (for example "-") | Not a dataset the scan lists | **R**: a phantom row. A user-property value with a newline prints lines for a name that does not exist. The duplicate-line check (zfs.go:466) fires only on a collision, and a phantom collides with nothing. Type "-" counts as a filesystem volume: `Volumes()` (table.go:175) yields it and `Kind()` says Filesystem. Probe: B `ListVolumes = [a(filesystem) a@ghost(filesystem)]`. | P2 rejects the scan (Internal) |
| R5 | '@' in ID but Type ≠ snapshot, or the reverse | The '@'-keyed `snapsOfVol` index and the Type-keyed `Volumes()` disagree, so one row is both a volume and a snapshot of another | **R**: same phantom as R4 | P2 rejects |
| R6 | A phantom with forged `managed`/`provision_success` *and source `local`* | The forger writes the source column too, so 5e5b2eb's "received is not local" does not apply to phantoms | **R**. Impact is listing pollution, a DeleteVolume block (a phantom snapshot with a CSI name, plan.go:394-396) and a CreateSnapshot `AlreadyExists` (plan.go:489-494). It **never causes data loss**: every op targets the phantom name and zfs says not-found. | P2 for partial forgeries. Residual: a *complete* forgery (a type line plus every column) still passes. The real fix is the `zfs list -H -o name` cross-check or `-j` (adv4/zfs-json.md plan). |
| R7 | `Managed`/`Committed`/`VolumeName`/`SnapshotName`/`SnapshotSource` as five free fields: Committed-but-foreign, managed-but-named-for-another, a managed snapshot with no CSI name | Nobody can say what such a row *is* without re-deriving it | **R** from ZFS state, and **the definitions disagree**. Take `v@volume-source-for-volume-x` with a local `managed_resource=true` and no CSI name: B `PlanDeleteVolume` counts it as internal (name-only `internalCloneSource`, plan.go:420) and plans `destroy -r` with no foreign-snapshot refusal, while B `PlanReconcile` skips it as "a CSI snapshot by ownership" (reconcile.go:208). Probe: `reach-drift.txt`. | P1: `classify` (P table.go) is the only place ownership is decided. That row is `Foreign`, so DeleteVolume refuses unless `-destroy-foreign-snapshots`. |
| R8 | `VolumeName` set to the literal "-" | `userValue` turns "set to '-'" into "unset" (endpoint.go:321-325). The rename guard accepts a row whose name marker is empty, so "-" passes as "no name". | **R** for anyone who can `zfs set` on a managed dataset (tampering, not accident) | Low. Read it as `p.Value` when `p.Local()`. Not prototyped. |
| R9 | `Creation` ≤ 0 | Unknown age | **R** only on a misparse. G fail-safe by `tooYoung`, which returns `SkipNoCreation` (reconcile.go:240-243). | Keep. 32bc4ec's test treats creation "-" as valid. |

### Table (table.go:84-158)

| # | Combination | Status | Fix |
|---|---|---|---|
| T1 | A one-volume table where a whole-parent table is needed | G by runtime `mustBeWhole` in CreateSnapshot (plan.go:485) and Reconcile (reconcile.go:112). **Not guarded in `PlanDeleteVolume`**: it checks `mustCover(id)` (plan.go:382), but `ClonesOf` (plan.go:400) needs every clone under the parent. Probe on B: a table covering only `src` plans `destroy -r tank/v/src` while `other` is a clone of `src@volume-source-for-volume-other` (`staged-partial-delete.txt`). Not reachable over RPC: DeleteVolume scans the whole parent (endpoint.go:453), and zfs refuses `-r` with dependent clones (the client never uses `-R`, zfs.go:253-255). | P3: `PlanDeleteVolume`, `PlanCreateSnapshot` and `PlanReconcile` take `FullTable`. A `Table` there no longer compiles. `mustCover` stays for per-volume coverage. |
| T2 | `NewTable(rows)` claims *whole* coverage by default (table.go:99-104) | The convenient constructor claims the strongest coverage. **U** in production: only table.go:330 and reconcile.go:193 call it. | P3: the AST test confines `NewTable`, `FullTable{…}` and non-empty `Row{…}` literals to table.go |
| T3 | `scan(ctx, only)` with `only == ""` meaning "everything" (table.go:293-296) | The empty string is overloaded. It is why coverage needed a flag. | P3: `scanVolume(id) Table` and `scanAll() FullTable` |
| T4 | CreateSnapshot reuses one variable `t` for the whole-parent table and then for a one-volume re-read (endpoint.go:561, 573). The re-read returns any row at the id, owned or not (endpoint.go:577-580); readVolume got an ownership check in 35fa482 (endpoint.go:430), CreateSnapshot did not. | **R** for the ownership half: a foreign writer re-creating the snapshot between apply and re-read | P3 forced `after := t.Table`. P1 adds `Own == SnapCSI`, else `Aborted`. |
| T5 | Duplicate IDs | **U**: `scanFor` dedups (endpoint.go:401-421) | Nothing |
| T6 | Zero `Table{}` | G: covers nothing (08b9699), so it fails safe | Nothing |

### Op and Plan (plan.go:13-42, apply.go)

| # | Combination | Status | Fix |
|---|---|---|---|
| O1 | Fields that don't fit the Kind: `Size`/`Sparse` off CreateVol, `Recursive`/`Defer` off Destroy, `Source` off Clone, `Props` on Destroy, tolerance flags on the wrong kind | **U**: every emit site fits (plan.go:237, 266-267, 410, 412, 539; reconcile.go:231). `apply` silently ignores fields that don't apply (apply.go:24-41). | R6 constructors |
| O2 | `OkMissing`: true at **4 of 4** destroy sites, never set elsewhere. `OkExists`: only on the internal clone-source snapshot (plan.go:266), since 35fa482 removed it from clones. | These are functions of the kind (plus one op), stored as free flags | R6: derive them in constructors, so "never adopt a racing clone" becomes structural |
| O3 | `Kind` zero or unknown | G by f266ee0 (apply.go:17-21). **The `exhaustive` linter cannot see this**: `OpKind` starts at `iota + 1` (plan.go:18), so `OpKind(0)` has no name. Run on bdf89d0, the linter was clean while the hole was open. | R6: name the zero value if the linter is to carry it |
| O4 | `Target`/`Source` outside `Parent` | **U** from planners. Probe: `apply` ran `destroy -r tank/other` against a canned runner. The namespace guard from future.md §2 is still not built. | future.md (not re-proposed) |
| O5 | A Plan that destroys and creates one target, or acts on a target after destroying it | **U**: no planner emits one. Reconcile's fixpoint emits only destroys. | R10 (defer) |
| O6 | `ReconcileAction.Target` (relative) duplicates `Op.Target` (full) (reconcile.go:69-75); server.go:213-215 derives the RPC op from `a.Op.Defer` and assumes `Kind == OpDestroy` | **U** | Derive Target from the Op |

### Request views

| # | Combination | Status | Fix |
|---|---|---|---|
| Q1 | `VolumeKind` out of range (it is an `int`, endpoint.go) | **R** from in-process Go callers. `VolumeKind(7)` skips every kind check in `validateCreate` (plan.go:98-125) and plans a filesystem, silently dropping `Reserve` and `Sparse` (probe: 2 ops, nil error). G for RPC by `kindIn` (server.go:37-42), which maps anything not BLOCK to Filesystem. | R7 `parseCreate` |
| Q2 | `Sparse` on a filesystem, `Reserve` on a block volume, both sources set, a block volume with capacity 0 | G by `validateCreate` (plan.go:107-121). It runs twice: endpoint.go:366 (the props are thrown away), then plan.go:136. Validated, not parsed. | R7: one parse, and `thick bool` per kind instead of two bools |
| Q3 | `NFSClient{Host: "*"}` | **R** from Go callers: the planner trusts whatever it is given, and the probe shows `sharenfs="rw=*"`. G for RPC by `ParseClient` (endpoint.go:669). The same type also holds *observed* etab hosts (export.go:192), where `*` and netgroups are legitimate: two meanings, one type. | R8 (with the ADR 0004 follow-up) |
| Q4 | `Desired{}`: `MinAge` 0 collects in-flight uncommitted volumes, a zero `Now` makes everything too young; `held == nil` means "no lock restriction" (reconcile.go:64-66) | G by `Reconcile` defaults (reconcile.go:324-326) and by code order (only the plan made under locks is applied) | R11, low |

### Responses, Config, errors

| # | Combination | Status | Fix |
|---|---|---|---|
| V1 | `Volume{Kind: Block, Mountpoint: …}`, or a filesystem with `DevicePath` | G: single constructor `volumeView` (endpoint.go:330-340) | Nothing. A proto `oneof` is not worth the wire change. |
| V2 | `Volume.SourceType` holding any string; `Volume.Name` empty (legacy rows without `csi_volume_name`, which `OwnedVolume` accepts) | **R** from ZFS state; cosmetic | Low |
| C1 | Zero `Config` | G: `New` checks Parent (endpoint.go:136-143) and prefix (`validPrefix`, endpoint.go:61, 143), and defaults the rest | Nothing |
| C2 | `Planner` literal without `VolBlockSize` | **R** from Go callers: `roundTo` divides by zero on a block create without volblocksize (plan.go:86-91; probe: panic). Only `New` builds a Planner in production (endpoint.go:163); tests use a literal (plan_test.go:13). | R11: guard `bs == 0` |
| E1 | `*Error` with Code 0 (`OK`) | **U**: every construction sets Code. **If one ever does not, the RPC reports success**: `status.Error(codes.OK, …)` returns nil (server.go:34; probe: `grpcErr = <nil>`). | R4: `CodeOf` maps a non-nil `*Error` with `OK` to `Internal` (one line, errors.go:48) |

---

## 2. Redesign options and what each costs

### P1. `Own`: one ownership enum (built)

```go
type Own uint8 // the zero value is Foreign: a literal, a phantom or an unclassified row is never acted on
const (
    Foreign Own = iota // no local managed marker, or markers naming another volume/snapshot (recv'd or renamed copy)
    VolUncommitted
    VolCommitted
    SnapCSI
    SnapCloneSource // unmarked "<vol>@volume-source-for-volume-<id>", ours by name only
)
func classify(typ zfs.Type, id string, m markers) Own // the only place ownership is decided
```

- **Removes:** R7 and the predicate drift. Planners `switch row.Own`, and `exhaustive -default-signifies-exhaustive=false` holds them to every case. The marker strings stay as data for views and messages.
- **Cost:**
  - Zero allocations; `Own` adds 1 byte to the row.
  - Two fields removed.
  - About 25 production lines change.
  - Tests now say what a row *is* (`Own: SnapCloneSource`) instead of which markers it carries. The marker-level rules are tested once, in `TestClassifyIsTotalAndAgreesWithThePredicates`: 288 marker combinations, checked against the fix builder's predicates.
- **Why not bit flags** (`Managed|Committed`): that brings back the 2ⁿ combinations.

### P2. The decode boundary (built, on top of 32bc4ec)

- Type allowlist; '@' ⇔ snapshot; per-type column groups filled only for the row's own type (P table.go `switch typ`).
- Failures are `Internal`: "refusing output zfs would not print". That matches the duplicate-line rule.
- **Cost:** +4.7 ms per 10k rows (13.7 → 18.4 ms best of 8), 0 extra allocations (scan: 275,237 allocs and 144 MB in both trees), about 1% of a scan.
- **Residual:** a complete forgery passes (see R6).
- **Also proposed, not built:** a completeness check in `zfs.parseColumns`, requiring every (dataset, property) pair to be printed exactly once. b540a2c's argument that "a forged line always collides" rests on zfs printing every requested property for every dataset, user properties included. My reading of `get_callback` in zfs_main.c says it does (it prints "-" with `ZPROP_SRC_NONE`). That reading needs one check on the 2.4 VM. The completeness check turns the assumption into a tripwire.

### P3. `FullTable` (built, on top of F21)

```go
type FullTable struct{ Table }                     // only scanAll and afterDestroy make one
func (e *Endpoint) scanAll(ctx) (FullTable, error)
func (e *Endpoint) scanVolume(ctx, id) (Table, error)
func (p Planner) PlanDeleteVolume(t FullTable, …)  // likewise PlanCreateSnapshot, PlanReconcile
func (t FullTable) afterDestroy(plans ...Plan) FullTable
```

- **Keeps** F21's runtime `covered` set for one-volume tables (`mustCover`). Only the whole/partial distinction moves into the type, because only it decides *which planner may run at all*.
- **Cost:** the struct embed is free, and `t.Table` downgrades explicitly. Five scan call sites change. F21's three runtime refusals become compile-fail cases. `PlanReconcile`'s error return now exists only for future use.

### Construction guard (built): the package is Go's only privacy boundary

Unexported fields stop nothing inside `package endpoint`, where every planner and test lives. The options:

- (a) Move Row, Table and decode into `internal/endpoint/state`. This gives a real boundary, but tests lose "every interesting state is one literal away" (plan_test.go:10-11), and planners need accessors. High churn.
- (b) **Chosen:** `TestOnlyTableGoBuildsState`, an AST walk that rejects non-empty `Row{…}`, `FullTable{…}` literals and `NewTable(…)` calls outside table.go and tests. It is 40 lines; a mutation check fails on all three fabrications.

### D. Kind-tagged column groups (measured, not built)

Measured on the dod3 10k fixture (501 filesystems, 500 zvols, 9,000 snapshots). Sizes: `Row` 432 B, `volRow` 384 B, `snapRow` 88 B; pointer variant `ptrRow` 88 B plus `volCols` 320 B or `snapCols` 40 B.

| Shape | Build (sort + index) | Iterate all snapshots | Snapshots of every volume | What it makes unrepresentable |
|---|---|---|---|---|
| flat `[]Row` (today) | 7.6–9.2 ms, 5,025 allocs (the `snapsOfVol` map of slices) | 290–350 µs | 0.49–0.55 ms | nothing |
| **split `[]volRow` + `[]snapRow`** | 4.9–5.3 ms, 32 allocs (a volume's snapshots are a contiguous ID range, so the map goes away) | **11–21 µs** | 0.72–0.78 ms (1,000 allocs from a `vol+"@"` concat; avoidable) | a volume with `Deferred`, a snapshot with a quota; `Get(volID)` returning a snapshot (the guards at plan.go:300, 304, 307 and reconcile.go:121 disappear); per-table `Own` subsets |
| pointer groups `Row{common; Vol *volCols; Snap *snapCols}` | 3.4–4.8 ms, **10,001 allocs** | – | – | nothing: both pointers can be non-nil, and nil checks and GC load are added |
| embedded value groups plus a kind tag | same size as flat | same | same | nothing beyond flat plus decode zeroing |

- **Verdict:** split arrays are the right *next* step after P1, for types rather than speed; every layout cost is at most 8 ms against a 300–450 ms scan.
  - Cost: `Get` becomes `Volume(id)`/`Snapshot(id)` at about 20 call sites, about 40 test literals change, and `simulateDestroy` walks two slices.
  - This does not conflict with the "don't build" list: it is one array of structs per kind, not struct-of-arrays, and two concrete slices, not a generic `Table[T]`.
- **Reject** pointer groups and value groups.

### E. Typed ids: `VolumeID`, `SnapshotID`, `Dataset`

- **Evidence of confusion risk:** `Row.ID` is relative and `Row.Name` full. `ClonesOf` takes a full snapshot name while `SnapshotsOf` takes a relative volume id (table.go:196-199). `ReconcileAction.Target` is relative while `Op.Target` is full. `scanFor(ids ...string)` mixes volume and snapshot ids (endpoint.go:401).
- **No live bug found.**
- `type VolumeID string` converts freely, so it names but proves nothing. A struct wrapper proves something only with the AST guard.
- **Cost:** about 60 sites (`volumeID` ×15, `parseSnapshotID` ×5, `Get` ×20, lock keys).
- **Verdict:** take the cheap 80%. Key the indexes by row, not by a string the caller must format (`t.ClonesOf(snapRow)`, `t.SnapshotsOf(volRow)`), and have `parseCreate` (R7) return typed ids. Skip a global migration.

### R6. Op constructors, keeping the fat struct

- `destroyOp(target, recursive)` and `deferDestroyOp(target)` set `OkMissing`.
- `createVolOp`/`createFSOp`/`cloneOp` have no tolerance argument: never adopt.
- `internalSnapshotOp` sets `OkExists`.
- The AST guard bans `Op{` outside ops.go.
- Name the zero value (`opInvalid OpKind = 0`) so `exhaustive` sees it.
- **Cost:** about 40 lines and nothing at runtime; printing and diffing are unchanged.
- Per-kind structs or a sealed interface stay rejected, for future.md §2's reasons: churn, an audit envelope, and loss of homogeneous logs.

### R7. Parse the create request once

```go
type createSpec struct {
    id     string     // volumeID'd
    kind   VolumeKind // only Filesystem or Block
    src    source     // none | fromSnapshot{vol, snap} | fromVolume{id}
    thick  bool       // block: !Sparse; filesystem: Reserve
    size, limit uint64
    props  map[string]string // canonical
}
func (p Planner) parseCreate(r CreateVolumeRequest) (createSpec, error)
```

- **Removes:** Q1 and Q2 as *states*, the double validation, and the `sourceOf` string pair (plan.go:542).
- **Cost:** about 60 lines; changes `PlanCreateVolume`'s signature and about 30 test calls.
- **Value:** medium, since it is reachable only from Go callers.

### R8. Exports: decode `sharenfs` into a type (the ADR 0004 follow-up)

- ADR 0004:27 says a local `sharenfs` that "parses strictly as our grammar is ours". That is the same move as `Own`.
- Decode into `Share{Off | Ours([]netip.Prefix per mode) | Foreign(raw)}`.
- Requested clients become `netip.Prefix` values, not strings. Observed etab entries become a separate `EtabEntry` type.
- **Cost:** it lands with the follow-up that removes `nfs_clients`, so nothing extra.

### Cheap one-liners

- **R4:** `CodeOf` treats a non-nil `*Error` with `OK` as `Internal`.
- **R11:** guard `roundTo` against `bs == 0`; give `Desired` a constructor.

### R10. A Plan builder that rejects contradictions

No planner can emit one today, and request plans are 1–4 ops. Build it when block export interleaves zfs and configfs ops in one plan (future.md §2).

---

## 3. Prototype (P = `wave5/types-copy`, diff `wave5/prototype.diff` against 9f1790d)

| File | Change |
|---|---|
| table.go (+198 −96, mostly comments) | `Own`, `classify`, strict `decodeColumns` (includes 32bc4ec's zvol rule), `FullTable`, `afterDestroy`, `scanAll`/`scanVolume`/`scanRoot`; `mustBeWhole` removed |
| plan.go (+44 −33) | `switch row.Own` in the resume branch and the DeleteVolume blockers; `FullTable` parameters; `sizing` replaces the fabricated rows; `internalCloneSource` removed |
| reconcile.go (+18 −22) | `switch r.Own`; `cur.afterDestroy(plans...)`; the orphan pass is `Own == SnapCloneSource` |
| endpoint.go (+19 −18) | scan split; the CreateSnapshot re-read checks ownership |
| tests (about 45 lines, mechanical) | `Own:` instead of marker booleans; `FullTable{…}` where whole coverage is required; coverage_test's three runtime cases moved to compile-fail |
| impossible_test.go (new) | see below |
| layout_bench_test.go (new) | §2 D benchmarks (`ZFS_FIXTURE=…/dod3/fixture.10k`) |

New tests, and what each proves:

- **`TestClassifyIsTotalAndAgreesWithThePredicates`**: across 288 combinations of type, id shape and markers, no snapshot is classified `Vol*` and no volume `Snap*`. `VolCommitted` matches `provision_success` exactly. The enum agrees with the fix builder's `OwnedVolume`/`OwnedSnapshot` everywhere. The one deliberate change, a marked snapshot never being `SnapCloneSource`, is asserted. `Row{}` is `Foreign`.
- **`TestDecodeRejectsWhatZfsNeverPrints`**: through `Endpoint.ListVolumes` over canned `zfs get` output, `Internal` is returned for:
  - a phantom snapshot-named volume;
  - a phantom without a type;
  - a filesystem with '@' in its name;
  - a snapshot without '@';
  - a bookmark;
  - a volsize of "12x".

  The control case lists one volume.
- **`TestDecodeZeroesColumnsTheTypeDoesNotHave`**: locally set `csi_volume_name`, `nfs_clients`, compression, refquota and mountpoint on a snapshot read as zero; a stray `csi_snapshot_name` on a volume reads as "".
- **`TestOnlyTableGoBuildsState`**: the AST guard. A mutation check on plan.go (adding `FullTable{t}`, `Row{ID: …}` and `NewTable(nil)`) produces three failures.
- **`TestBadStatesDoNotCompile`**: the real compiler, via `go build -overlay -gcflags=-e`, rejects:
  - `Row{Managed: true}`;
  - `r.Committed`;
  - a `Table` passed to `PlanDeleteVolume`, `PlanCreateSnapshot` or `PlanReconcile`;
  - `e.scan(…, "")`;
  - a `Row` passed to `sizeOps`.

  A control build with the intended shapes compiles. The test takes 3.9 s and is skipped under `-short`.

Results:
- `go vet ./...` is clean.
- `go test -race -count=1 ./...` passes across csi, endpoint, server, unixsock, zfs and cmd (`final-test.txt`). This includes the fix builder's ownership, foreign, prefix, coverage and decode tests, unchanged in substance.
- `exhaustive -default-signifies-exhaustive=false ./internal/endpoint/` is clean (v0.12.0, built against x/tools v0.52.0 because v0.15.0 doesn't compile on Go 1.27).
- gofmt is clean.

Behaviour changes in P, all deliberate:
1. A snapshot under the clone-source name *with* a local managed marker and no CSI name is `Foreign`. DeleteVolume refuses it unless `-destroy-foreign-snapshots`, where B destroyed it with `-r`. Reconcile still leaves it alone.
2. The CreateSnapshot re-read requires `SnapCSI`, else `Aborted`.
3. The scan refuses rows with a type outside the three listed, or a name that disagrees with its type (`Internal`).
4. `PlanDeleteVolume`, `PlanCreateSnapshot` and `PlanReconcile` take `FullTable`.

Two things the reachability probes still show reachable in P, all from in-process callers only: `VolumeKind(7)`, the zero-`Planner` panic, `NFSClient{Host: "*"}` (R7, R11, R8). `apply` outside `Parent` is the future.md item.

---

## 4. Don't build

- **Pointer column groups.** 10,001 allocations per 10k rows, and both pointers can still be set, so nothing becomes unrepresentable.
- **Embedded value groups plus a kind tag.** The same as flat with decode zeroing, but more syntax.
- **Moving Row and Table to a subpackage for privacy.** It costs "one literal away" in tests. The AST guard gives the same guarantee for 40 lines.
- **A whole-codebase typed-id migration now.** About 60 sites and no live bug. Key indexes by row and type the parsed create request instead.
- **Per-kind Op structs or a sealed interface.** Unchanged from future.md §2.
- **A Plan builder now.** Nothing can emit a contradiction yet; build it with block export.
- **A proto `oneof` for Volume's location.** A single constructor already guards it, and it would change the wire format.
- **`Own` as bit flags.** That brings back the boolean product.
- **Relying on `exhaustive` for zero-value kinds.** It cannot see an unnamed `iota + 1` zero; name it or keep the runtime check.
- **A single World, a generic `Table[T]`, struct-of-arrays.** Unchanged; the split per-kind arrays in §2 D are none of these.
