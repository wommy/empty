# DOD review of future work: worlds, ops, audit, verification, and where to stop

Scope: future edges of the Go ZFS endpoint (NFS export, block export, reconcile/GC, ADR 0001 auth, ADR 0002 ids, node side, observability, multi-parent). Read-only review.

Citation pins:
- `HEAD` = `b540a2c` for `internal/endpoint/{table,plan,apply,reconcile,props,endpoint}.go`.
- `FE:` = branch `file-export` (`28e48dc`) for `export.go` and the export flows in `endpoint.go`.

Note: the working tree is mid-merge of `file-export` (status `UU` on endpoint.go, proto, README), so line numbers in the tree may drift from these pins. `docs/research/gaps/block-export-v2.md` does not exist on any branch (`git log --all` finds nothing). The block design reviewed is `block-export.md` plus `adv-block-design.md` and `adv-block-kernel.md`.

---

## 0. What the code already tells us (facts the recommendations rest on)

| Fact | Where | Why it matters for future work |
|---|---|---|
| `apply`'s `switch op.Kind` has **no `default`**. An unknown or zero `Kind` leaves `err == nil` and counts as applied. | `apply.go:16-39` | Every new op kind (configfs, share) that is planned but not wired into `apply` is a silent success. This is the biggest latent risk as the vocabulary grows. |
| `OpKind` has no `String()`. `opName` maps CreateFS and CreateVol to the same `"create"`. | `apply.go:45-59` | Audit and golden output would print integers, or lose information. |
| No golden plan tests exist. Tests poke `plan[0].Props["..."]`. | `plan_test.go:69-70,96,130`; `props_test.go:109-119,206-208` | README's "fat struct for golden tests" decision (`README.md` decisions table) is a claim that has not been cashed in yet. |
| A `Table` does not record what it covers. `scanFor` unions per-volume scans into a Table that looks the same as a full one. | `table.go:61-80`; `endpoint.go:304-322` | Planners that act on absence (`PlanCreateSnapshot`'s global-name loop at `plan.go:397`, `PlanDeleteVolume`'s clone check, any future export diff) are correct only because a comment makes the caller do a full scan (`endpoint.go:349-350`, `:447-448`). |
| `ReconcileAction{Op, Target, Reason, Volumes}` and `ReconcilePlan{Actions, Skipped}` already exist, with DryRun. | `reconcile.go:69-97`, `:297`, `:327` | The audit record and the dry-run shape already exist for one RPC. Generalise them; do not invent new ones. |
| Reconcile plans, locks, re-scans, and re-plans under the locks, then applies the **second** plan. | `reconcile.go:322-352` | A dry-run plan is not the plan that runs. Audit must log what actually ran. |
| `simulateDestroy` is a second interpreter of `Op`, a hand model of zfs destroy and defer semantics. | `reconcile.go:256-290` | Each new op kind raises the question "simulate it too?" (answer in §5). |
| NFS: `etab` is a planner input (`PlanPublishExport(t Table, live Etab, …)`). Post-apply verification is inline code that re-reads etab, duplicated in the two RPCs. | FE `export.go:318`, `:356`; FE `endpoint.go:586-592`, `:630-636` | This is the first second-source edge, so it sets the pattern. |
| `FileEtab` returns `nil, nil` for a missing etab file, and a `nil` Etab is the same as an empty one. | FE `export.go:246-252` | "Not observed" and "observed empty" are one value. That is fine here (a missing file really means nothing is exported), but it is wrong as a general pattern (§1). |
| Row keeps widening per edge. NFS added 5 columns (`ShareNFS`, `ShareNFSLocal`, `Mounted`, `NFSClients`, `NFSClientsSet`). Block will add `Guid`, `block_id`, `block_id_guid`, and an export record. | FE `table.go:29-35`; `adv-block-design.md` #2 | The branch was cut before `property-allowlist`. `columns()` is a literal on FE and `append(..., propNames[:]...)` on HEAD (`table.go:125-132`), so every edge collides on the same two functions. |
| Unscoped "missing → OK". `PlanDeleteVolume` returns `nil, nil` when the row is absent. | `plan.go:317-320` | If ADR 0001's scope is implemented by filtering rows, an out-of-scope delete becomes "missing → OK", which ADR 0001 explicitly forbids. |

---

## 1. One "World" struct, or per-edge inputs?

### Recommendation
Do **not** build one `World` struct that every planner takes. Apply the same discipline to every source: read once, decode once into an immutable typed value, record its coverage, and never let a planner treat "not read" as "empty". Express it with **per-edge input structs** whose fields name exactly the sources that planner reads. Policy is not a source; it is a check on the planner's *output* (§1c).

### Sketch

```go
// Every observed source carries the same header. This is the "same shape" that pays.
type Obs struct {
    At       time.Time // per source: sources are never read atomically together
    Coverage Coverage  // Full, or Only{vols}: what absence means
}

type Coverage struct{ only map[string]bool } // nil = full parent

func (c Coverage) Full() bool              { return c.only == nil }
func (c Coverage) Covers(vol string) bool  { return c.only == nil || c.only[vol] }

// ZFS stays the Table it is; it gains the header.
type Table struct {
    Obs
    rows        []Row
    snapsOfVol  map[string][]int
    clonesOfSrc map[string][]int
    byBlockID   map[string][]int // block edge (adv-block-design #2, #10)
}

// Host-global sources: one value per read, for every parent and edge.
type Etab struct {
    Obs
    byPath map[string][]NFSClient
}

type Configfs struct { // flattened from the tree: rows, not a tree
    Obs
    Backstores []CfsBackstore // name, udev_path, serial, enabled
    Targets    []CfsTarget    // iqn/nqn, tpg enabled, attrs
    LUNs       []CfsLUN       // target, lun, backstore link
    ACLs       []CfsACL       // target, initiator, mapped lun
    Ports      []CfsPort      // shared; refcount derived (adv-block-design #6)
    byBlockID  map[string]int
}

// Per-edge planner inputs: the type IS the declaration of what is read.
type ExportNFSIn struct {
    T    Table
    Etab Etab
}

type ExportBlockIn struct {
    T   Table
    Cfs Configfs
}

func (p Planner) PlanPublishExport(in ExportNFSIn, vol string, c NFSClient) (Plan, error)
func (p Planner) PlanBlockExport(in ExportBlockIn, vol string, acl ...) (Plan, error)
func (p Planner) DiffExports(in ExportBlockIn, scope []string) (Plan, []Drift)

// Absence-acting planners assert coverage instead of trusting a comment.
func (p Planner) PlanDeleteVolume(t Table, id string) (Plan, error) {
    if !t.Full() { return nil, errorf(Internal, "delete planned on a partial table") }
    // ...
}
```

### Why not one `World{ZFS, Etab, Cfs, Policy}`? Where it breaks

1. **Lifetimes and scopes differ.**
   - The zfs Table is per parent.
   - `etab`, configfs, nvmet `ports/` and `hosts/` are per host and shared across parents and with foreign tools (`adv-block-design.md` #6).
   - Policy is per process and changes on SIGHUP (ADR 0001 §2).

   A single struct implies one consistency point that does not exist: the zfs scan and the etab read in FE `endpoint.go:568-575` are two syscalls apart. A per-source `Obs.At` is honest about this. A `World.At` is not.
2. **"Not read" collapses into "empty".** If `CreateVolume` is handed a `World` whose `Cfs` was never walked, because nobody needed it, the zero value means "no exports". The first planner that later learns to look at exports (for example DeleteVolume refusing an exported zvol, `adv-block-design.md` #8) silently plans against "nothing exported" and gets "dataset is busy" on every retry. Worse, an unpublish planner gets "not live" and answers OK while the export is still live. Per-edge input types make the missing read a compile error.
3. **Partial coverage is the dangerous case, not the struct shape.** `adv-block-design.md` #6 is exactly this: a per-request `scanFor(vol)` Table diffed against a full configfs walk tears down every other export. A uniform `World` hides that; `Coverage` on each source exposes it. The rule: **a planner may act on absence only within `Coverage`.**
4. **Planner signatures stop documenting dependencies.** `func(World, Req)` everywhere means every planner test builds a World, and code review can no longer see that Unpublish reads etab.
5. **Multi-parent daemons.** One World per parent duplicates the host-global sources and lets two parents' diffs disagree about shared ports. The correct shape is host-level sources read once and per-parent Tables, joined in a per-edge input.

### What the uniform discipline buys
- One rule to fuzz: "never act on absence outside Coverage". It slots into `FuzzReconcileSafe`.
- The same `Obs` header can be audited (§3) and fingerprinted.

### Failure modes of the recommendation
- **Coverage set wrong by the shell.** `scanFor` must set `Only{ids}`, and `scan(ctx, "")` must set Full. One test per scan entry point covers it.
- **Per-edge input structs multiply.** Expect 3 to 5, not dozens. If a sixth appears, reconsider.
- **Host-global sources need a host-level lock** that the per-volume `lock.Keyed` cannot give (`adv-block-design.md` #10). Coverage does not fix concurrency.

### Cost
- `Coverage` on Table plus asserts in about 4 planners: around 40 lines.
- Per-edge input structs: free, since they replace extra positional args.

### Priority
**P0** for `Coverage`, before any configfs diff or export reconcile lands. **P1** for the per-edge input pattern, which NFS already approximates (`PlanPublishExport(t, live, …)`).

### 1c. Policy (ADR 0001) is not a Table
- **Recommendation.** Two pure checks, and neither one filters rows before planning:
  - `AuthorizeRequest(scope, rpc, ids…)`: a prefix check on request ids, run before the scan.
  - `AuthorizePlan(scope, plan)`: every `Op.Target` and `Op.Source` must map to an in-scope volume, run after planning and before apply.
- **Why.** Filtering the Table by scope turns an out-of-scope DeleteVolume into `plan.go:317-320` "missing → nil", which is OK to the caller, the exact failure ADR 0001 forbids. Checking the output plan is fail-closed even for a planner that forgot the scope: ADR 0001's own "known risk" (scope enforcement is split, a new entry point forgets it) disappears for mutations, because every mutation passes through `apply`.
- **Gap left.** Read paths (`List*`, `Get*`) still need explicit filtering. Long snapshot ids (ADR 0002) resolve post-scan, so the output check catches a clone from another tenant's `zs1-` snapshot through `OpClone.Source`.
- **Cost.** About 60 lines, plus a test that every op kind's target-to-volume mapping is defined (the same exhaustiveness hook as §2).
- **Priority.** **P0**, shipping with ADR 0001.

---

## 2. Op vocabulary growth: fat struct, tagged union, or per-edge slices?

What actually gets added:
- **configfs mutations:** mkdir, write, symlink, unlink, rmdir. Five kinds, per `block-export.md` DESIGN.
- **`sharenfs` changes:** these are `OpSet` already (FE `export.go:304`, `:311`), so no new kind.
- **etab checks and policy checks:** these are **reads and predicates, not ops**. Making them ops (an `OpCheckEtab`) would put observation inside `apply`, the same objection `adv-block-design.md` #10 raises against `ReadFirst`.

So the vocabulary goes from 6 to about 11 kinds across **two execution domains**:
- zfs CLI, with errors classified by `zfs.ErrExists` and `zfs.IsNotFound` (`apply.go:34-36`);
- configfs syscalls, with errno EEXIST, ENOENT, EBUSY, EINVAL and EACCES (`adv-block-kernel.md` 2b, 12).

### Judged by the four criteria

| Criterion | Fat struct (`[]Op`, enum Kind) | Sealed interface per kind | Per-edge slices (`Plan{ZFS []ZOp; Cfs []COp}`) |
|---|---|---|---|
| Golden diffability | Good **once** `Op.String()` prints only the fields that kind uses, with sorted props. `%+v` prints dead zero fields. Not built yet (§0). | Same: each type gets a `String()`. | Good per slice, but the cross-slice order is invisible. |
| Replay tolerance | `OkExists`/`OkMissing` map onto both domains (EEXIST/ENOENT). Write-once attributes are handled by **the planner omitting the write** (`adv-block-design.md` #7), not by a third flag. | The same flags, but per type, so they can't be set on the wrong kind. | Per-slice flags. |
| Audit logging | Best: one homogeneous row type, so one JSON schema. | Needs a marshal per type, or a tagged envelope. | Two schemas, plus a separate order. |
| Exhaustiveness | `exhaustive` lints enum switches by default (`dod-go/core-shell-raft.md` §3). Field validity per kind is **not** checked by the type system. | Type switches are linted only with `-check=typeswitch`. Field validity is enforced by the type. | Per slice. |
| **Cross-domain ordering** | Native. | Native. | **Broken.** Block needs the zfs record written before the configfs build (`adv-block-design.md` #8), and the configfs teardown before `zfs destroy` (the IBLOCK holder makes the destroy "busy", #8). Two slices force a hard-coded "zfs then cfs" or "cfs then zfs" order. |

### Recommendation
Keep **one ordered `[]Op` with a fat struct** and an enum `Kind` spanning both domains. Add four guards:

```go
type OpKind uint8 // go:generate stringer; exhaustive-linted

const (
    OpCreateFS OpKind = iota + 1
    // ... zfs kinds ...
    OpDestroy
    OpCfsMkdir
    OpCfsWrite
    OpCfsSymlink
    OpCfsUnlink
    OpCfsRmdir
)

type Domain uint8

const (
    DomZFS Domain = iota + 1
    DomCfs
)

func (k OpKind) Domain() Domain

type Op struct {
    Kind      OpKind
    Target    string            // zfs: full name under Parent; cfs: path RELATIVE to cfs root
    Source    string            // clone origin; symlink target (relative)
    Value     string            // cfs write
    Size      uint64
    Sparse, Recursive, Defer bool
    Props     map[string]string
    OkExists, OkMissing bool
    Secret    bool // Value is a reference, never a secret; see §3
}

// Validate is the type system the fat struct lacks: fuzzed, and called by apply.
func (o Op) Validate(parent string) error // field-kind consistency + Target namespace

func (e *Endpoint) apply(ctx context.Context, plan Plan) ([]Outcome, error) {
    for i, op := range plan {
        if err := op.Validate(e.cfg.Parent); err != nil {
            return out, internal(i, err)
        }
        switch op.Kind.Domain() {
        case DomZFS:
            err = e.zfsExec(ctx, op) // has its own default: Internal
        case DomCfs:
            err = e.cfsExec(ctx, op) // root-relative; never absolute paths
        default:
            return out, internal(i, "unknown op kind")
        }
        // tolerance mapping per domain: zfs.IsNotFound vs errors.Is(err, fs.ErrNotExist)
    }
}
```

The four guards:
1. **`default:` returns Internal in apply** (fixes the `apply.go:16` hole), plus `exhaustive -default-signifies-exhaustive=false` in CI.
2. **`Op.Validate`** fuzzed as part of `FuzzCreateConverges` and `FuzzReconcileSafe`. It rejects a `Size` on a non-CreateVol, a cfs `Target` that is absolute or contains `..`, and a zfs `Target` outside `Parent+"/"`. This namespace guard is what stops a type-confused bug from ever running `zfs destroy` on a configfs path or `rmdir` on a dataset name.
3. **Per-domain error mapping.** `zfsErr` mislabels configfs errnos (`adv-block-design.md` #10).
4. **`OpKind.String()`**, and `Plan.String()` printing only the fields each kind uses, with sorted props. Then write the golden tests the README already claims.

### Failure modes
- The fat struct still allows `Defer` on a cfs op. `Validate` catches it at runtime and in fuzzing, not at compile time.
- `Props map` makes `Op` non-comparable (`==` panics through interfaces, and `reflect.DeepEqual` is needed). That is already true today.
- Five cfs kinds may tempt "one `OpCfs` with a sub-op string". Don't: it defeats `exhaustive`.

### Cost
- `stringer`, `Validate`, the default case and the lint: about 120 lines.
- No churn in the tests that read `plan[0].Props`.
- Golden files: about 1 day for the existing planners.

### Why not the sealed interface now
`dod-go/core-shell-raft.md` recommends it. The benefit is compile-time field validity. The cost is churn in `ReconcileAction.Op` (`reconcile.go:70`), `simulateDestroy`'s field reads (`reconcile.go:266-283`), every test literal (`reconcile_test.go:275-280`), and a JSON envelope for audit. `Validate` plus fuzzing gives about 90% of the safety at about 10% of the cost. Revisit if a third domain appears, for example node-side mounts in the same Plan, which §5 argues against.

### Priority
- **P0**: the default case and `String()`. Both are tiny and fix a live hazard.
- **P1**: `Validate`, the domain split and the namespace guard. These land with the first cfs op.

---

## 3. Plans as the audit and observability substrate

### Recommendation
Generalise `ReconcileAction` (`reconcile.go:69-75`) into the return type of every mutating planner:

```go
type Action struct {
    Op
    Reason string
    Locks  []string
}
```

- The Reason strings are stable constants, as in `reconcile.go:27-43`.
- Write one **intent record** before `apply` and one **result record** after it, to an append-only JSONL file **outside the pool**. ZFS user properties vanish with the object, and `zpool history` records the daemon, not the caller (`edges2/observability-audit.md`, Audit).
- Derive every metric from `Outcome`s. Do not instrument planners.

### Minimal record

```go
type PlanRecord struct { // one line, written and fsynced BEFORE apply iff Destructive()
    ID        string    // random 128-bit; joins intent to result
    At        time.Time
    Principal string    // ADR 0001: "unix:uid=0" | SPIFFE URI
    RPC       string    // "DeleteVolume"
    Parent    string    // multi-parent daemons
    Req       string    // canonical request ids only (volume id, snapshot id, host); no free text
    Inputs    []ObsRef  // {source:"zfs"|"etab"|"cfs", at, coverage:"full"|"vol:a,b", hash}
    Actions   []Action  // exactly what apply will run (post-lock re-plan, not the dry run)
    DryRun    bool
}

type ResultRecord struct {
    ID       string
    Outcomes []Outcome // {Index, Result: Done|Tolerated|Failed|NotRun, Code, Dur}
    Residual []Action  // §4: what a re-plan still wants; empty = verified
}
```

### Rules
- **`Destructive()` is derived, not hand-flagged.** It is true when the kind is in {Destroy, CfsRmdir, CfsUnlink}, or when an `OpSet` touches `sharenfs`, the `nfs_clients` user property, or an ACL write. Access revocation is destructive too: Unpublish removes a client's access. Only destructive plans pay for the fsync. Everything else is logged asynchronously.
- **Secrets never enter an Op.** CHAP passwords (`adv-block-design.md` #3) appear as `Value: "secretref:<id>"` with `Secret: true`, and the cfs executor resolves the reference from the 0600 store. If secrets were inlined, the audit log would become a credential dump, since the log records Ops verbatim. Enforce it in `Op.Validate`: a cfs write under `auth/` or to `dhchap_key` requires `Secret`.
- **Log the applied plan.** `Reconcile` re-plans under locks (`reconcile.go:347-352`). The record carries the second plan; the first is the dry run's.
- **Empty plans are not records.** Idempotent retries are the majority of CSI traffic. Count them as a metric (`plans_total{rpc,empty="true"}`), and don't log them.

### Metrics, all derived from `Outcome`, with no dataset names in labels (`observability-audit.md` label rule)
- `zfsep_ops_total{kind,domain,result}`, where result is done, tolerated or failed.
  - **`tolerated` is the most useful signal.** It counts replays (crash recovery) and out-of-band changes (EEXIST/ENOENT that the plan did not expect). Nothing else in the system can see these.
- `zfsep_plan_ops{rpc}`: a histogram of plan length. Spikes mean a reconcile mass action.
- `zfsep_destructive_plans_total{rpc}`.
- `zfsep_verify_residual_total{rpc}` (§4).
- `zfsep_exec_seconds{subcommand}`, timed around the runner, as zrepl does.

### Dry run everywhere?
- **Yes** for Reconcile (it exists already) and for export reconcile at startup (report-only, per `reconcile-gc.md`).
- **No** for CSI RPCs: CSI has no dry-run and nothing would call it.
- Instead, add **one admin RPC**: `Explain(rpc, request) → []Action`. It plans without locks and states that the result is advisory, because the applied plan is re-planned under the locks.

### Failure modes
- **Audit disk full or slow.** The intent write fails, so the destructive apply must fail. That is fail-closed and changes availability, so put it behind a flag defaulting to on, with a metric for it.
- **Crash between intent and result.** The log holds an intent without a result. A startup pass can mark those "unknown" and attach the next scan's residual.
- **Req redaction.** Only ids are logged. `Properties` maps are validated allowlisted values, so they are safe to log, but keep them out of the first version.

### Cost
About 200 lines (record, writer, fsync policy, metrics) plus the `Action` refactor across 6 planners. Reasons already exist as constants for reconcile; the other planners need about 10 more.

### Priority
**P1**. It ships with ADR 0001, because a principal without a record is half the value.

---

## 4. Verification as data: a second pure function?

### Recommendation
Do not write a separate `Verify(desired, observed)` per edge. The planner already **is** the desired-versus-observed function. Post-apply verification is **re-observe, then re-plan with the same request, under the same locks; the plan must be empty.** A non-empty residual becomes `Unavailable`, carries the residual Actions as diagnosis, and is logged in `ResultRecord.Residual`.

```go
// in the shell, generic:
func (e *Endpoint) verify[In any](ctx context.Context, observe func(context.Context) (In, error),
    replan func(In) (Plan, error)) (Plan, error) {
    in, err := observe(ctx)
    if err != nil {
        return nil, err
    }
    residual, err := replan(in)
    if err != nil {
        return nil, err
    }
    if len(residual) > 0 {
        return residual, errorf(Unavailable, "applied, but %s still differs: %s", residual[0].Reason, residual)
    }
    return nil, nil
}
```

### Check against the NFS code: it already works
- **Publish** (FE `export.go:318-350`). After apply, the row holds the client, so `exportOps` is empty. If etab doesn't serve the client, the planner emits `reshare` (`:344-347`). The residual is non-empty, which reproduces exactly the inline check at FE `endpoint.go:589-592`.
- **Unpublish** (FE `export.go:356-386`). If the client is still live after apply, the planner emits `reshare`, which reproduces FE `endpoint.go:633-636`.
- The two hand-written checks, and their duplicated message logic, go away. The message comes from the residual's `Reason` ("export table does not serve client"), so add Reasons to `reshare`.
- **CreateVolume** already re-reads (`endpoint.go:300`, `readVolume`). Re-planning on that table is free and turns `FuzzCreateConverges`' invariant into a runtime assertion.

### Where it does not generalise (be precise)
1. **Time-dependent planners.** `PlanReconcile` uses `MinAge` and `Now` (`reconcile.go:235-243`). A re-plan can pick up rows that aged in or appeared since. Restrict the residual to the **targets of the applied actions**.
2. **Immutable drift.** For block, a mismatch on a write-once attribute is reported as `Drift`, not an op (`adv-block-design.md` #7). The residual becomes `(Plan, []Drift)`. Drift is a status, not a failure of this call.
3. **Error-returning post-states.** A planner that refuses its own post-state (for example `PlanCreateSnapshot` after a racing out-of-band snapshot) returns an error, not a residual. Map that to Internal with both plans in the record.
4. **Same-model blindness.** If the planner misreads a source, it misreads it twice, and the re-plan agrees with the bug. Only a different observer catches this: the sim and real-ZFS CI for zfs, and a real nfsd or configfs in CI for the others. Verification-by-re-plan is a runtime safety net, not a correctness proof.
5. **Silent-success sources.** This is the rule that matters: **any source whose writes can report success while not taking effect must be an input to the planner that writes it.** `zfs set sharenfs` exits 0 on exportfs failure (FE `export.go:25-27`), which is why etab is a planner input and why re-plan verification works there at all. For configfs, writes fail loudly, but restored configs and foreign writers make it a required input anyway (`adv-block-design.md` #1).
6. **Asynchronous sources.** If a deployment reloads exports asynchronously (a path unit rather than libzfs's synchronous `exportfs -ra`, `gaps/file-export.md` (c)), the re-plan races the reload. Give verification a bounded poll (for example 3 tries over 500 ms) for sources marked `Async` in `Obs`. Do not do this for zfs.

### Cost
- **Code:** one generic helper, and the removal of about 30 lines of inline checks.
- **Runtime:** one extra observation per mutating call.
  - The CreateVolume re-read already exists. A DeleteVolume re-check needs a scoped scan (about 1.5 ms of exec, `README.md` decisions).
  - A configfs re-walk is the expensive one. Limit it to the touched export's subtree, with Coverage set accordingly.

### Priority
**P1**. Adopt it when merging FE, so the pattern is set before block arrives.

---

## 5. Where DOD is over-engineering here (adversarial to DOD)

| Place | Plain code wins because | Keep / do |
|---|---|---|
| **Policy (ADR 0001)** | A few principals, each with an RPC set and a prefix. A table or World column adds nothing, and filtering rows by policy is actively dangerous (§1c). | A pure `authorize` function and a method table, exactly as ADR 0001 writes it. |
| **etab** | Tens to hundreds of lines. FE's `map[path][]NFSClient` with a linear `Lookup` is right. Hostname matching (`Lookup`'s `host+"."` prefix, FE `export.go:219-226`) is semantics, not indexing. | Keep it as is; only add `Obs`. |
| **LIO/nvmet create and teardown order** | The order is fixed by kernel constraints (`adv-block-kernel.md` 1e, 4a). Write it as straight-line planner functions (`planLIO(e) Plan`). **Do not** build a dependency graph and topo-sort it Terraform-style (`dod/reconcile-diff.md` shows Terraform's diff does not even give the edges). | Hand-ordered emitters, with golden tests. |
| **Simulating plans on the Table** | `simulateDestroy` (`reconcile.go:256-290`) already re-implements zfs destroy and defer semantics. Extending "simulate" to `OpSet` sharenfs, configfs or ACLs means writing a kernel model inside the planner, which is a second sim to keep honest. | Keep `simulateDestroy` narrow, for destroy only. For export reconcile, plan one round, apply, re-observe (§4), and let the next run do the rest. It runs off the request path, so extra rounds are cheap. |
| **A generic `Table[K,R]`** | Two real tables (zfs, configfs) and one map (etab) have different keys, indexes and joins. A generic one now freezes the wrong abstraction. | Revisit at the third instance. |
| **Owner goroutine, cached Table, SoA, interning, channel-program IR** | Already measured or rejected: layout is about 12 µs per 10k rows against about 1.5 ms per exec (`README.md`); ZFS is authoritative, so a cache is stale state (`dod-go/iterators-indexes-concurrency.md`); ZCP can't create or set volsize (`dod/channel-programs.md`). | Stay rejected. Re-raise only with a benchmark. |
| **Event-sourced audit** | Replaying a log to rebuild state duplicates ZFS, the source of truth (ADR 0003). The log is for humans and forensics. | Append-only JSONL, no replay. |
| **Node side (attach and mount)** | The work is waiting: the device node appears asynchronously (`adv-block-design.md` #9), iscsid logs in, the mount retries. Plan/apply fits poorly with polling against kernel timing, and the node has no concurrency peer to plan against. | Read `/proc/self/mountinfo` and sessions as typed values (cheap, testable). Write the attach as plain idempotent steps with explicit timeouts. Do not put node ops in the controller's `Plan`. |
| **Request validation** (`ParseClient`, `normalizeHost`, `ValidateProperties`) | It is parsing, not data layout. | It is already code. Keep it that way. |
| **A persistent desired-state table or reconcile loop** | Rejected with evidence (`dod/reconcile-diff.md`): CSI is synchronous, and a desired table drifts from ZFS. | Keep request-scoped plans, plus caller-supplied desired sets for GC. |
| **Dry run on every RPC** | Nothing calls it, and it lies about the post-lock plan (§3). | `Explain` admin RPC only. |

**Where DOD still earns its keep and must not be "simplified away":**
- the single scan per source per request, because it gives one consistent read for the planner;
- immutable tables;
- `Coverage`;
- pure planners, which make the §4 re-plan verification free;
- plans as data, which make §1c output authorization and §3 audit nearly free.

These four pay for everything above.

---

## 6. Cross-cutting: multi-parent daemons

- **Lock keys** are `vol:<id>` on a per-Endpoint `lock.Keyed` (`endpoint.go:90`, `:205`). For separate Endpoint instances per parent, zfs keys are fine. Host-global objects (nvmet ports and hosts, the LIO np) need one **process-wide** keyed lock, with keys like `cfs:port:<id>`, shared by all parents' Endpoints. A per-parent lock cannot serialise them.
- **Ownership discriminator.** The IQN/NQN prefix must include a hash of the parent (`adv-block-design.md` #6). Otherwise parent A's diff sees parent B's targets as "owned extras". `Coverage` does not help here, because the configfs walk is "full" but covers several owners. Add `Owner` to the decoded `Configfs` rows, and make the diff act only on `Owner == self`.
- **Audit and metrics** need a `Parent` field and label. The parent count is bounded, so it is a safe label.

---

## Ranked recommendations

1. **P0** `apply.go:16`: add `default:` returning Internal, plus the `exhaustive` lint and `OpKind.String()`. A new op kind that isn't wired into apply is a silent success today.
2. **P0** Record `Coverage` on `Table` (and on every observed source), and have planners that act on absence assert it. `scanFor` partial tables are indistinguishable from full ones today, and the block diff (#6 in the review) will delete everything through this hole.
3. **P0** ADR 0001 scope: a request-id check plus an `AuthorizePlan` over every `Op.Target`/`Source`. Never filter rows by policy, which turns an out-of-scope delete into "missing → OK" (`plan.go:317-320`).
4. **P1** Verification = re-observe and re-plan under the same locks, and the residual must be empty. Replace FE's inline etab checks (FE `endpoint.go:586-592`, `:630-636`). Restrict the residual to applied targets for reconcile, and return `Drift` for immutable configfs attributes.
5. **P1** One ordered `[]Op` fat struct spanning domains (zfs and cfs), with `Kind.Domain()`, per-domain executors and errno mapping, `Op.Validate` (field-kind consistency plus a Target namespace guard), and the guard fuzzed. Reject per-edge slices, which break cross-domain ordering, and defer the sealed interface.
6. **P1** Per-edge planner input structs (`ExportNFSIn{T, Etab}`, `ExportBlockIn{T, Cfs}`), not one `World`. The type declares which sources are read, so "not read" can never masquerade as "empty".
7. **P1** Generalise `ReconcileAction` into `Action{Op, Reason, Locks}` for every planner. Write a `PlanRecord` (principal, rpc, parent, input fingerprints, actions) fsynced before destructive applies, and a `ResultRecord` after, as JSONL outside the pool. Secrets go in by reference only.
8. **P2** Derive metrics from op outcomes. `ops_total{kind,domain,result}` with `result=tolerated` is the replay and out-of-band-drift signal nothing else gives.
9. **P2** Group per-edge Row columns (`Row.NFS`, `Row.Block`) with per-group decode. FE already conflicts with property-allowlist in `columns()`, and block adds four more columns.
10. **P2** Golden plan tests via `Plan.String()`. README's fat-struct justification is not yet backed by a single golden test.
11. **P2** Multi-parent: a process-wide lock for host-global configfs objects, an owner discriminator in IQN/NQN, and `Parent` on audit records and metric labels.
12. **Don't:** a single World, a generic `Table[T]`, a dependency-graph or topo-sort for configfs, extending `simulateDestroy` to new ops, an event-sourced audit log, node-side ops in the controller Plan, dry-run on CSI RPCs, or an owner goroutine, SoA or ZCP IR.
