# Volume and snapshot lifecycle as an explicit FSM, checked exhaustively

Targets:
- **HEAD** = `bdf89d0`.
- **POST** = branch `fixes-wave3` at `1782874`, which already contains CHECKPOINT items A–F21:
  - ownership: `OwnedVolume`/`OwnedSnapshot`, received is not local;
  - clone sizing derived once; `OpClone` without `OkExists`;
  - op-aware error codes (killed = Aborted/DeadlineExceeded);
  - default prefix `zfs-endpoint`;
  - `apply` default case; table coverage;
  - foreign-snapshot refusal in DeleteVolume/Reconcile (`d4ec271`).

  The branch moved from `3107a48` to `1782874` while this ran. All POST results below were re-run on `1782874`.

The repo was not modified. Work copies are under `scratchpad/wave5/`:

| Copy | What it is |
|---|---|
| `fsm-copy/` | HEAD + explorer |
| `fsm-copy-fix2/` | POST + explorer |
| `fsm-copy-fix2-exp/` | POST + the delete-order change proposed in §5 R1 (diff: `fsm-delete-order.diff`) |

Explorer: `internal/endpoint/fsm_explore_test.go` (+ `fsm_shim_test.go`, which models `zfs recv`: a local set on HEAD, whose decode cannot tell the difference, and `Sim.Receive` on POST).

```
cd scratchpad/wave5/fsm-copy-fix2
FSM_DEPTH=4 FSM_KIND=fs|block FSM_PREFIX=zfs-endpoint|democratic-csi FSM_OUT=/path/report.txt \
  GOTOOLCHAIN=go1.27.3 go test ./internal/endpoint/ -run TestFSMExplore -v
```

Reports: `scratchpad/wave5/{head,fix2,fix2exp}-{fs,block}[-prefix]-d4.txt`.

---

## 1. States

Ownership markers are user properties under the configured prefix. They count only when the source is `local`; the POST fix also excludes `received`.
- **M** managed_resource
- **C** provision_success
- **VN** csi_volume_name
- **Src** csi_volume_content_source_type and csi_volume_content_source_id
- **SN** csi_snapshot_name
- **SS** csi_snapshot_source_volume_id

Two ZFS facts also feed the states: `origin` and `defer_destroy`. Two more are derived from the table: whether a snapshot has clones, and whether its name has the reserved prefix `volume-source-for-volume-` ("vsfv-").

### 1a. Volume rows (filesystem or zvol)

| State | M | C | VN | Src | origin | Reached | Planners treat it as |
|---|---|---|---|---|---|---|---|
| **Absent** | – | – | – | – | – | yes | create target; delete = OK |
| **Foreign** | ∅ | ∅ | ∅ | ∅ | any | yes (admin create) | refuse (AlreadyExists / FailedPrecondition / NotFound) |
| Foreign(recv) | recv'd | recv'd | recv'd | | | yes | **HEAD: owned** (Committed or Alias). POST: Foreign |
| Foreign(dcsi) | (other prefix) | | | | | yes | POST default prefix: Foreign |
| **Adopted-democratic-csi** | dc:T | dc:T | =id | ∅ | ∅ | yes | prefix=democratic-csi (adopt mode): same as Committed. The distinction is provenance only. |
| **Created-uncommitted** | T | ∅ | =id | ∅ | ∅ | yes | CreateVolume resumes; Reconcile collects |
| **Clone-uncommitted** (clone pending commit) | T | ∅ | =id | snap:X / vol:V | X / V@vsfv-id | yes | resume; collect |
| **Committed** | T | T | =id | ∅ | ∅ | yes | serve; compat check |
| **Committed-clone** | T | T | =id | set | matches Src | yes | serve |
| Committed-detached | T | T | =id | set | ∅ | no (needs admin `promote`) | serve |
| Alias (renamed or copy) | T | any | ≠id | | | no (needs rename; recv is classified by provenance) | HEAD: **owned** by Delete/Expand/Snapshot/Reconcile. POST: Foreign |

### 1b. Snapshot rows

| State | M | SN | SS | deferred | clones | name | Reached |
|---|---|---|---|---|---|---|---|
| **CSI-snap** | T | set | =vol or ∅ | no | any | any non-vsfv | yes |
| **CSI-snap-deferred** (Deferred-destroy) | T | set | =vol | yes | ≥1 | | yes |
| **CloneSrc-live** | ∅ | ∅ | ∅ | no | ≥1 | vsfv-<id> | yes |
| **CloneSrc-orphan** (Clone-pending-source-snap) | ∅ | ∅ | ∅ | no | 0 | vsfv-<id> | yes: crash after op 1 of CreateVolume(from volume), or **after op 1 of DeleteVolume(clone)** |
| CloneSrc-deferred | ∅ | ∅ | ∅ | yes | ≥1 | vsfv-<id> | only with proposal R1 (transient) |
| **Foreign-snap** | ∅ | ∅ | ∅ | any | any | other (sanoid, admin), or any snapshot on a Foreign volume | yes |
| Foreign-snap(recv) | recv'd | | | | | | yes; HEAD reads it as CSI-snap |
| Alias-snap | T | set | ≠vol | | | | no (rename) |

### 1c. Impossible combinations (map to no state)

The explorer classifies every observed row and reports any of these as `unnamed-state`.

| # | Combination | Who could produce it | Reached? |
|---|---|---|---|
| I1 | ¬M ∧ (C ∨ VN ∨ Src): markers without the managed marker | HEAD race: `OpClone OkExists` swallows EEXIST on a foreign dataset, then stamps `provision_success` on it | **HEAD yes** (M=∅,C=T). POST no |
| I2 | M ∧ VN=∅ | nobody (neither the endpoint nor democratic-csi writes it), yet `OwnedVolume` accepts it | no |
| I3 | half source (type xor id) | nobody | no |
| I4 | M ∧ Src=∅ ∧ origin≠∅ | admin clones our snapshot and marks it | no |
| I5 | M ∧ ¬C ∧ Src≠∅ ∧ origin=∅ | promote of a half-provisioned clone | no |
| I6 | Src≠∅ ∧ origin ≠ the snapshot Src names | promote of a sibling clone re-parents | no |
| I8 | ¬M snapshot ∧ (SN ∨ SS) | nobody | no |
| I9 | M snapshot ∧ SN=∅ | nobody; HEAD would still let DeleteSnapshot destroy it | no |
| I10 | M snapshot under the vsfv- prefix | refused by CreateSnapshot | no |
| I11 | deferred ∧ no clones | zfs reaps it at once (sim does too) | no |
| I12 | two live owned snapshots with one SN (cross-row) | HEAD: a recv'd copy b@s shadows a@s (CreateSnapshot retry → AlreadyExists after DS(a@s)) | HEAD via recv; POST no |
| I13 | clone whose origin is a Foreign snapshot | HEAD `CV(b<-a@autosnap)` | **HEAD yes**; POST NotFound |
| I14 | CSI-snap on a Created-uncommitted volume (pair) | CreateSnapshot checks ownership, not commit | **both** (unintended, not unsafe) |

I2 and I14 are acceptance gaps: the planners accept a state the FSM says should not exist.

---

## 2. Transitions

The notation is (state) —op→ (state). `⚡k` marks a crash after op k.

### 2a. RPC plan steps (POST; HEAD differences in brackets)

| Request | From | Plan ops (`Op` kind, tolerance) | Intermediate → final |
|---|---|---|---|
| CreateVolume plain | Absent | 1 `OpCreateFS`/`OpCreateVol` -o M,VN,(sharenfs/sharesmb=off, refquota[,refres]) | Absent → **Created-uncommitted** |
| | | 2 `OpSet` C=true | → **Committed** |
| | Created-uncommitted (same src/kind/bs) | [`OpSet` size] [`OpSet` props] `OpSet` C | → Committed |
| | Committed | none (compatible) or AlreadyExists | – |
| | Foreign / Alias / other kind or source | AlreadyExists | – |
| CreateVolume ← snapshot V@s | Absent, V@s CSI-snap of a **Committed** V [HEAD: any snapshot row, incl. Foreign-snap and uncommitted V] | 1 `OpClone` -o markers [HEAD: OkExists] | → **Clone-uncommitted** |
| | | 2 `OpSet` size (fs quota from source, derived once) [HEAD: resume path forgets the source quota] | |
| | | 3 `OpSet` C | → Committed-clone |
| CreateVolume ← volume V | Absent, V Committed [HEAD: any managed V] | 1 `OpSnapshot` V@vsfv-id (OkExists) | → CloneSrc-orphan |
| | | 2 `OpClone` [HEAD OkExists] | → Clone-uncommitted + CloneSrc-live |
| | | 3 `OpSet` size | |
| | | 4 `OpSet` C | → Committed-clone |
| DeleteVolume | any owned volume state | 1 `OpDestroy -r` (OkMissing) | → Absent (with its snapshots) |
| | | 2 if origin is vsfv: `OpDestroy -d` origin (OkMissing) | CloneSrc-live → CloneSrc-orphan (after 1) → gone |
| | refusals | CSI-snap present (FailedPrecondition); dependent clone elsewhere (FailedPrecondition); Foreign-snap present [HEAD: destroyed]; Foreign (FailedPrecondition) | |
| | Absent | none, OK | |
| ExpandVolume | owned volume (incl. **uncommitted**) | `OpSet` volsize / refquota[,refres] | same state |
| CreateSnapshot | owned volume (incl. **uncommitted**, I14) | `OpSnapshot` -o M,SN,SS | Absent → CSI-snap; SN live elsewhere → AlreadyExists; same id deferred → AlreadyExists |
| DeleteSnapshot | CSI-snap | `OpDestroy -d` (OkMissing) | → Absent (no clones) / CSI-snap-deferred |
| | deferred / absent / never-issued id | none, OK | |
| | Foreign-snap | FailedPrecondition | |
| Reconcile | Created-/Clone-uncommitted older than MinAge | DeleteVolume plan | → Absent (+ release vsfv origin) |
| | Committed ∉ desired (desired non-empty) | DeleteVolume plan | → Absent |
| | CloneSrc-orphan on an owned volume | `OpDestroy -d` | → gone |
| | anything blocked by PlanDeleteVolume | skip with reason | |

### 2b. Crash and kill transitions (what the checker injects)

- **Process death after op k** (k = 0..n). k = n means the plan completed and the response was lost.
  - Every intermediate in the table above is reachable.
  - The new ones are: Created-uncommitted, Clone-uncommitted, CloneSrc-orphan (CreateVolume←volume ⚡1, DeleteVolume(clone) ⚡1), and the partial-set states below.
- **Deadline kill of `zfs`**: the command either ran or did not, and the endpoint lives on and answers.
  - HEAD answers **Internal** (13).
  - POST answers Aborted/DeadlineExceeded.
- **Kill mid `zfs set a=…,b=…`**: any proper subset applied. Example: refquota set, refreservation not.
- **Kill mid `destroy -r V`**: V's snapshots gone, V left in its prior volume state. Live CSI-snaps never get here, because the planner refuses them first. Orphan clone sources and, on HEAD, Foreign-snaps go first, and the retry finishes the job.

### 2c. Out-of-band transitions (as steps, and as races injected before every op)

| Actor | Transition |
|---|---|
| admin `zfs create` | Absent → Foreign |
| sanoid `zfs snapshot V@autosnap` | adds a Foreign-snap to any volume |
| `zfs send -R a \| zfs recv b` | Absent → Foreign(recv) (+ b@s Foreign-snap(recv)). HEAD decodes it as a managed, committed Alias. |
| democratic-csi create | Absent → Adopted (adopt prefix) / Foreign(dcsi) (default POST prefix) |
| admin `zfs promote` (not explored) | Committed-clone → Committed-detached (I6 for siblings) |
| admin `zfs rename` (not explored) | → Alias |
| admin `zfs inherit <marker>` (not explored) | → I1/I2 |

---

## 3. Exhaustive checker

### What it explores

**World.** A world is a list of zfs commands replayed onto a fresh `zfstest.Sim`. The sim clock is deterministic. Worlds are deduplicated by a fingerprint: every column except creation time, plus provenance.

**Search.** Breadth-first from an empty parent, depth 4, over two alphabets.
- 13 RPCs:
  - `CV(a)`, `CV(b,reserve)`, `CV(b<-a@s)` (cap 0), `CV(b<-a,reserve)` (cap 0), `CV(b<-a@autosnap)`;
  - `CS(s,a)`, `CS(s,b)`, `DS(a@s)`, `DV(a)`, `DV(b)`, `EV(a,2G)`;
  - `Reconcile{}`, `Reconcile{a}`.
- 4 out-of-band actions:
  - `X:admin-create(b)`, `X:sanoid(a@autosnap)`, `X:recv(a->b)`, `X:dcsi-create(a)`.

**Per (world, RPC) edge:**
- the uncrashed run;
- a crash at each boundary 0..n, plus a kill mid every multi-prop `set` (all proper subsets) and mid every `destroy -r`. Each is followed by **one retry**, and the crash intermediates are enqueued too (so "crash, then a different RPC" is covered);
- a deadline kill before and after each op;
- every OOB action injected as a race before each op. Per-volume locks do not cover other actors.

**Per reachable world (crash intermediates included):** two reconciles, desired={} and desired=every committed volume, each run twice.

### Assertions

1. **unnamed-state**: every row maps to a named state (§1).
2. **retry-diverges / retry-response**: crash + retry ends in the same fingerprint and the same response as the uncrashed run. When it does not, the checker also tries "then Reconcile" to see if that recovers.
3. **foreign-touch**: no mutation targets a Foreign row.
   - Targets are the set/destroy target, every row `-r` covers, a snapshot's volume, and a clone's origin and its volume.
   - Checked live, before each command, against the current world.
4. **reconcile-unsafe / reconcile-not-idempotent**: reconcile never destroys a CSI-snap, a Foreign row, or a desired committed volume. A second reconcile plans nothing and changes nothing.
5. **kill-final-code**: once an op of a plan has run, a deadline kill must answer a code external-provisioner retries (Aborted, Unavailable, DeadlineExceeded, ResourceExhausted, Canceled).

### Results (depth 4; "runs" counts crash+retry pairs only)

| Config | distinct worlds | edges | crash/retry runs | findings |
|---|---|---|---|---|
| HEAD fs (prefix dcsi) | 604 | 2964 | 10514 | **41** |
| HEAD block | 439 | 2184 | 7442 | **34** |
| POST fs, zfs-endpoint | 224 | 1443 | 4612 | 4 |
| POST fs, democratic-csi (adopt) | 367 | 2145 | 7194 | 7 |
| POST block, zfs-endpoint | 185 | 1222 | 3676 | 4 |
| POST block, democratic-csi | 289 | 1703 | 5322 | 7 |
| POST + R1 (all four configs) | 197–425 | 1248–2275 | 3504–7116 | **0** |

Each run takes 13–52 s.

### Counterexamples: minimal traces, grouped by root cause

**CX1 – DeleteVolume of a volume-clone leaks its clone source (HEAD and POST, fs and block).**
- Trace: `CV(a) ; CV(b<-a,reserve) ; DV(b) ⚡1/2 ; retry DV(b)`.
- Uncrashed end state: `a:Committed`.
- Retried end state: `a:Committed a@volume-source-for-volume-b:CloneSrc-orphan`, and the retry answers OK.
- Cause: the retry finds b Absent → empty plan. The orphan pins a's space until the next Reconcile.
- `Reconcile{committed}` does recover it (`collected=a@volume-source-for-volume-b`), in all 32 instances.
- This is the only crash-convergence failure left at POST. All 7 POST retry findings are this one (×prefix×extra rows).

**CX2 – sanoid snapshot destroyed through a race between scan and `destroy -r` (HEAD and POST).**
- Trace: `CV(a) ; DV(a) [race: X:sanoid(a@autosnap) before op 1/1]`
  ⇒ `zfs destroy -r tank/k8s/vols/a` touches `a@autosnap [Foreign-snap]`.
- POST's refusal (`d4ec271`) closes the non-race path. HEAD also has these non-race variants:
  - `CV(a) ; X:sanoid ; DV(a)`;
  - `… ; Reconcile{}` collecting an uncommitted a;
  - the kill-mid-`destroy -r` variant.
- POST still loses the snapshot to the race, because the refusal is judged on the scan and `-r` acts on the live tree.

**CX3 – HEAD clone `OkExists` adopts a foreign dataset (fixed in POST, `35fa482`).**
- Trace: `CV(a) ; CV(b<-a,reserve) [race: X:admin-create(b) before op 1/4]`
  ⇒ `zfs set refquota=… refreservation=… tank/k8s/vols/b` touches `b [Foreign]`.
- End state is impossible state **I1** (`b: M=∅ C=T`).
- The same happens with `X:recv(a->b)` and with the block variant (`set provision_success=true` on b).

**CX4 – HEAD clone-size resume bug (fixed in POST, `725c328`; fs only).**
- Trace: `CV(a) ; CS(s,a) ; CV(b<-a@s) ⚡1/3 ; retry` → b committed with `refquota 0`. Uncrashed: `refquota 1 GiB`.
- Same for `CV(b<-a,reserve) ⚡2/4`.
- Same for a kill mid `set refquota,refreservation` (mask 1 → `refreservation 0` is never repaired).
- Knock-on idempotency break (`retry-response`): `CV(a) ; CV(b<-a,reserve) ⚡2/4 ; CV(b<-a,reserve)` → OK, then the identical request → **AlreadyExists (6)**, because the committed b has reserve=false.

**CX5 – HEAD treats foreign or received data as its own (fixed in POST).**
- `CV(a) ; X:sanoid(a@autosnap) ; CV(b<-a@autosnap)`: clones (pins) an admin snapshot (I13).
- `CV(a) ; X:recv(a->b) ; CS(s,b)`: snapshots a received copy.
- `CV(a) ; X:recv(a->b) ; Reconcile{a}`: `destroy -r` of the received copy b.
- `CV(a) ⚡1/2 ; X:recv(a->b) ; Reconcile{}`: collects the copy as "uncommitted".

**CX6 – HEAD deadline kill answers Internal after a partial plan (fixed in POST, `2fadc0c`/`6d4043a`).**
- Trace: `CV(a) DEADLINE-KILL(op 1/2, ran)` → 13.
- The same holds for every mutating RPC (13 RPC kinds, fs and block): `CV*`, `CS`, `DS`, `DV`, `EV`, `Reconcile`.
- Internal is final for external-provisioner: a PVC deleted after that answer strands the half-made dataset.

There were no `reconcile-not-idempotent` findings in any config, and no `unnamed-state` at POST.

**Proposal R1 verified.** With POST + R1 (§5) all four configs show 0 findings. The only other effect is 3 unit tests that assert action order or shape (`TestPlanDeleteVolume`, `TestPlanReconcile` ×3 subtests, `TestReconcileAgainstDesiredSet`); none is a behavioural regression.

### What the checker does not cover

- Concurrent RPCs. Per-volume locks serialize every pair that shares a volume, and Reconcile re-plans under its locks.
- NFS export, block export, node side.
- `rename`, `promote`, and `inherit` of markers. So Alias, Committed-detached, I2–I6 and I8–I10 are argued, not reached.
- A real pool. Sim fidelity is per `docs/research/simfidelity/`, and `destroy -r` partial order follows zfs: snapshots first.

---

## 4. HEAD vs POST semantics (what the model changes)

| Aspect | HEAD `bdf89d0` | POST `1782874` |
|---|---|---|
| Ownership source | `local` or `received` | `local` only |
| Owned volume | `Managed` | `OwnedVolume`: M ∧ VN∈{∅,id} (Alias → Foreign) |
| Owned snapshot | `Managed` | `OwnedSnapshot`: M ∧ SN≠∅ ∧ SS∈{∅,vol} |
| Clone source | any snapshot row (incl. Foreign-snap, CloneSrc-*) / any non-snapshot row | CSI-snap of a Committed owned volume / Committed owned volume |
| `OpClone` | OkExists | fails AlreadyExists (no adoption race) |
| Clone size | uncrashed and resume paths differ (CX4) | `sourceSize` once, before the resume branch |
| Killed or unknown failure mid-plan | Internal | Aborted / DeadlineExceeded (op-aware; busy destroy → FailedPrecondition) |
| Default prefix | democratic-csi (DC volumes are simply Committed) | zfs-endpoint (DC → Foreign(dcsi)); `-property-prefix democratic-csi` = adopt mode |
| Foreign snapshots on DeleteVolume/Reconcile | destroyed by `-r` | refused / skipped unless `-destroy-foreign-snapshots`; **still lost to the scan→destroy race (CX2)** |
| Unknown `OpKind` in apply | silent success | refused (F20) |
| Table coverage | implicit | recorded; absence-acting planners require full (F21) |
| Unchanged in both | CX1 orphan on DeleteVolume ⚡1; I14 (snapshot or expand of an uncommitted volume); I2 accepted | |

---

## 5. Redesign: a first-class FSM inside the DOD shape

### 5.1 One `State` column per Row, derived at parse time

```go
type State uint8 // go:generate stringer; exhaustive-linted

const (
    StInvalid State = iota // impossible combination; Row.Why says which (I1..I13)
    StForeign              // not ours: unmarked, received, other prefix, alias (POST rules)
    StUncommitted          // M, ¬C, no source
    StCloneUncommitted
    StCommitted            // includes adopted; provenance stays a separate column if ever needed
    StCommittedClone
    StCommittedDetached
    StSnapCSI
    StSnapCSIDeferred
    StSnapCloneSrcLive
    StSnapCloneSrcOrphan
    StSnapCloneSrcDeferred
    StSnapForeign
    nStates
)

type Row struct { /* existing columns */ State State; Why string }
```

- `decodeColumns` computes the per-row part: markers, source, provenance.
- `NewTable` finishes the cross-row part (clones-of index → live/orphan/deferred; origin ↔ Src consistency) in one pass after it builds the indexes. The DOD shape is preserved: the result is one more column, set once.
- `OwnedVolume`/`OwnedSnapshot`/`internalCloneSource` and about 37 `.Managed`/`.Committed`/`.Deferred`/prefix tests across `plan.go`, `reconcile.go`, `endpoint.go` and `export.go` (28 at HEAD) become `row.State` comparisons.
- **StInvalid is refused everywhere** (FailedPrecondition with `Why`) and reported by Reconcile as a skip. That closes I2/I14-style acceptance gaps by construction.

### 5.2 Planners switch on (State, Request) with an exhaustive table

```go
type Req uint8 // CreatePlain, CreateFromSnap, CreateFromVol, Delete, Expand, Snap, DeleteSnap,
               // RecUncommitted, RecUndesired, RecOrphan, Get/List(visibility)

type Cell struct {
    Ops   []OpKind // the op kinds this transition may emit, in order (cited, checked)
    To    State    // state after the full plan (StAbsent for deletes)
    Mid   []State  // states legal after each crash point; retry from each must reach To
    Err   Code     // non-OK: refusal, no ops
    Plan  func(p Planner, t Table, row Row, r Request) (Plan, error) // details only (sizes, props)
}

var fsm = [nStates + 1][nReqs]Cell{ // +1 = StAbsent; a zero Cell is a test failure
    StAbsent: {
        CreatePlain:    {Ops: ops(OpCreateFS|OpCreateVol, OpSet), Mid: sts(StAbsent, StUncommitted), To: StCommitted, Plan: planCreate},
        CreateFromVol:  {Ops: ops(OpSnapshot, OpClone, OpSet, OpSet),
                         Mid: sts(StAbsent, /*src*/ StSnapCloneSrcOrphan, StCloneUncommitted), To: StCommittedClone, Plan: planCloneVol},
        Delete:         {To: StAbsent}, // OK, no ops
        ...
    },
    StUncommitted: {
        CreatePlain: {Ops: ops(OpSet, OpSet, OpSet), Mid: sts(StUncommitted), To: StCommitted, Plan: planResume},
        Snap:        {Err: NotFound},  // I14 closed: not a volume yet
        Expand:      {Err: NotFound},
        Delete:      {Ops: ops(OpDestroy), To: StAbsent, Plan: planDelete},
        RecUncommitted: {Ops: ops(OpDestroy), To: StAbsent, Plan: planDelete},
    },
    StCommittedClone: {
        Delete: {Ops: ops(OpDestroy /*-d origin*/, OpDestroy /*snaps*/, OpDestroy), // R1 order
                 Mid: sts(StCommittedClone /*origin deferred*/), To: StAbsent, Plan: planDelete},
    },
    StForeign: { /* every Req */ CreatePlain: {Err: AlreadyExists}, Delete: {Err: FailedPrecondition}, ... },
    ...
}

func (p Planner) Plan(t Table, r Request) (Plan, error) {
    row, _ := t.Get(r.ID())
    c := fsm[row.State][r.Kind()]
    if c.Err != OK { return nil, refusal(c.Err, row) }
    plan, err := c.Plan(p, t, row, r)
    if err == nil && !kindsMatch(plan, c.Ops) { return nil, internal("plan %v outside cell %v", plan, c.Ops) }
    return plan, err
}
```

- The detail functions are today's planner bodies minus their ownership and branching prologues: about 60% of `PlanCreateVolume` is the `if row, ok … !Managed … !Committed …` ladder.
- Cross-row preconditions (dependent clones, global CSI name, foreign snapshots under the volume) stay in `Plan` funcs, but each returns a code that the cell declares in an `AllowErr` field. They are not hidden ad hoc branches.
- `kindsMatch` is a subsequence check: optional size or prop sets may be skipped. It runs in tests always and in production as a cheap assertion (it complements F20's apply default).

### 5.3 Each table row cites its Op kinds

`Cell.Ops` is the citation. Two greps answer "which transitions can emit `OpDestroy -r`" and "which ops can a Foreign row ever see": `Cell.Ops` and `Err`. Rendered with `go generate` into a markdown table in `docs/`, the table *is* §2a. The table also replaces Reconcile's candidate filter: `RecUncommitted`/`RecUndesired`/`RecOrphan` are cells, so `PlanReconcile` becomes "for each row, look up its cell".

### 5.4 Tests generated from the table

One table-driven test replaces the hand-picked cases:

1. **Exhaustive**: every `[State][Req]` cell is non-zero, and `exhaustive` lints the State switches.
2. **Fixture per State**: `fixture(State) []Row` builds a minimal table in that state. The explorer already has the builders: `CV`, crash points, OOB actions.
3. **Per cell**:
   - apply the plan to a pure interpreter: generalise `simulateDestroy` into `simulate(rows, op)` for all six kinds;
   - assert `kinds ⊆ Ops`, `State(after) == To`;
   - for each prefix of the plan, assert `State ∈ Mid`, then re-plan from it and reach `To` (this is check 2 above, per cell, in microseconds);
   - assert no op targets a row whose State is Foreign or Invalid.
4. **Keep the explorer** (this file, trimmed to about 500 lines over the sim) as the slow nightly/fuzz layer for multi-step sequences, races and reconcile. Its `classify` becomes the production `State` derivation, so model and code cannot drift.

### 5.5 Cost and what it replaces

| Item | Size |
|---|---|
| `State` derivation in decode/NewTable + Why codes | ~150 lines |
| `fsm` table (≈13 states × 11 reqs, mostly `Err` one-liners) | ~200 lines |
| Planner refactor: prologues → cells | plan.go −150/+120, reconcile.go −80/+40 |
| Pure op interpreter (generalise `simulateDestroy`) | ~150 lines (replaces ~40) |
| Generated cell tests + fixtures | ~300 lines; supersede most of plan_test.go and the ownership/clonesize/foreign case lists (~600 lines today) |
| Explorer, trimmed, nightly | ~500 lines (exists) |

Estimate: **3–4 focused days, about net zero lines.**

It replaces:
- the ~37 scattered ownership predicates;
- `OwnedVolume`/`OwnedSnapshot` as separate rules;
- Reconcile's candidate filter;
- `simulateDestroy` as a delete-only second interpreter;
- the hand-maintained "which ops can touch what" knowledge in comments.

Do it after `fixes-wave3` merges. The table encodes POST semantics; doing it first would freeze HEAD's bugs into cells.

---

## 6. Top 3 recommendations

1. **R1: make DeleteVolume crash-convergent and race-proof by changing its op order and dropping `-r`.** The plan becomes:
   1. `destroy -d <vsfv origin>` first (zfs keeps it while the clone lives and reaps it with the clone);
   2. `destroy` each snapshot the plan judged;
   3. `destroy` the dataset without `-r`.

   This fixes CX1, the orphan that a retry cannot see. It also fixes CX2, the sanoid snapshot taken by the race: a late snapshot now fails the last op instead of being destroyed.
   - Verified: 0 findings in all 4 POST configs (`fsm-copy-fix2-exp`, `fsm-delete-order.diff`, ~25 lines).
   - Cost: 3 order-asserting tests to update.
   - A deferred CSI snapshot in the explicit list keeps `-d`. Map "has children" on the final destroy to FailedPrecondition naming the new snapshot.
2. **R2: close the acceptance gaps the FSM exposes.**
   - Refuse Invalid combinations (I2: managed without a name marker; the rest of I1–I13) instead of treating them as owned.
   - Make CreateSnapshot, ExpandVolume and GetVolume on an **uncommitted** volume NotFound (I14), as ListVolumes and POST's clone-source check already do. Today a half-provisioned volume can be snapshotted and expanded under an id the CO was never given.
3. **R3: land the `State` column plus the (State, Request) table after `fixes-wave3` merges** (§5), with generated per-cell crash/retry tests.
   - Keep this explorer as a nightly depth-4 job over {fs, block} × {zfs-endpoint, democratic-csi}: ≈2 minutes total, and it fails on any finding.
   - At HEAD it would have caught CX3–CX6 before review. At POST it catches CX1/CX2 today.
