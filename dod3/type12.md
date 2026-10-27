# Type I / type II audit of the ZFS endpoint's decisions

Code: `b540a2c` (HEAD of `claude/gallant-johnson-1qaxp5`). The working tree has an unfinished `file-export` merge with conflict markers, so this audit used a clean `git archive HEAD` copy at `scratchpad/dod3/err-copy`, with Go 1.27.3. OpenZFS: `a128b8a` (2.4.99). The version-sensitive tables were also checked against 2.2.9 and 2.4.0. Per ADR 0003, OpenZFS is the evidence. Nothing in the repo was edited.

**Framing.** Each decision is a predicate over untrusted data, such as zfs stderr, `zfs get` rows or caller ids.
- **Type I** means the predicate fires when it should not. Examples: "owned" for a foreign row, "safe to destroy", "absent", "compatible".
- **Type II** means it fails to fire when it should.

For each decision the ledger records:
- what each error does concretely, and its cost;
- which way the code errs today;
- the default it should take when the input is uncertain, chosen by the cost asymmetry.

Every "OBSERVED" claim comes from a probe that ran in the copy (`internal/endpoint/type12_probe_test.go`, `go test -run TestProbe -v`).

Cost scale: data-loss 5 > exposure 4 > leak = stuck = silent-success 3 > wrong-final 2 > retry-storm = diagnostic 1. Score = (cost of the error the code makes today) × reach, where reach is common 3, admin-action 2, race 1.

## Ledger (sorted by score; JSON: `scratchpad/dod3/errors-ledger.json`)

| # | decision | site | predicate | type I (false positive) | type II (false negative) | cost I / II | errs now | default on uncertainty | score |
|---|---|---|---|---|---|---|---|---|---|
| D10 | DeleteVolume refusal set (snapshots / clones / holds) | internal/endpoint/plan.go:321-334 | refuse iff unmanaged, or a snapshot with local csi_snapshot_name and not deferred, or a clone outside the volume; else destroy -r | (predicate 'safe to destroy' wrongly true) foreign snapshots (sanoid, zfs-auto-snapshot, replication bases) do not block: destroy -r deletes them with the volume | (wrongly refuses) unmanaged dataset -> FailedPrecondition retried forever (spec-legal, PV stuck) | data-loss / stuck | I | fail closed: refuse (FailedPrecondition, naming them) while any snapshot not owned by us exists; classify held -> FailedPrecondition | 15 |
| D05 | row -> owned vs foreign | internal/zfs/zfs.go:43 (Prop.Local) -> internal/endpoint/table.go:153-158 (own) | ownership property Source in {local, received} | received copy (zfs recv / send -R backup under the parent) reads Managed+Committed: DeleteVolume plans destroy -r; Reconcile with any non-empty desired set collects it; its csi_snapshot_name rows make the original's CreateSnapshot AlreadyExists | an owned volume reads foreign: DeleteVolume FailedPrecondition forever, CreateVolume AlreadyExists, residue leaks | data-loss / stuck | I | fail closed: Source == local only; plus consistency (VolumeName == ID, SnapshotSource == vol) before any destroy | 10 |
| D01 | zfs stderr -> error Kind | internal/zfs/errors.go:107-148 (patterns, classify); zfs.go:116-121 | first of 22 substrings found in lower(stderr), table order; else KindUnknown | decisive Kind for the wrong condition: destroy EEXIST race prints 'dataset already exists' -> Exists -> AlreadyExists on DeleteVolume; 'no such parent' -> ParentNotFound -> NotFound (CreateVolume NOT_FOUND means 'source missing', final) | real condition unclassified -> Unknown -> Internal (final): pool is read-only, dataset is read-only, encryption root's key is not loaded, it's being held, 'must be one of', 'must be power of 2', 'read-only property', unsupported version or feature, module does not support, I/O error ... 130 of 162 in-scope decisive OpenZFS strings | wrong-final / leak | II | absence: stay closed (only explicit not-found is absent). Retry class: an Unknown AFTER the first mutating op of a plan = outcome/residue uncertain -> retryable (Unavailable) so the CO re-drives the idempotent plan; Unknown BEFORE any mutation -> Internal (nothing to leak) | 9 |
| D08 | request props vs existing row: compatible vs AlreadyExists | internal/endpoint/props.go:113-115 (canon tables), 211-225 (propsMismatch) <- plan.go:153 | every requested prop == row.Local[k] as a string (canonical form from the 2.4 table) | (compatible wrongly true) xattr=on canonicalised to 'sa', but on 2.2 'on' means dir: caller silently gets different semantics | (incompatible wrongly) xattr=dir on 2.2 userland reads back 'on' -> every identical retry AlreadyExists, forever; snapdir=disabled on 2.2 -> "'snapdir' must be one of" -> Internal | silent-success / stuck | II | evidence-sourced tables per detected userland version; unknown version -> refuse version-sensitive values at validate (InvalidArgument) instead of create-then-stuck | 9 |
| D09 | clone/restore source: owned vs foreign | internal/endpoint/plan.go:181-188 (snapshot source), 206-216 (volume source) | source row exists, right type, not deferred (no Managed/Committed check) | foreign source accepted: endpoint cuts '<foreign>@volume-source-for-volume-<id>' on a dataset it does not own, clones it, hands the data to the caller; the foreign owner can no longer destroy it (dependent clone) | owned source refused: restore fails NotFound (final), PVC pending until fixed | exposure / wrong-final | I | fail closed: require src.Managed && src.Committed; snapshot source needs snap.Managed && SnapshotName != ''; else NotFound (same answer as an id never issued) | 8 |
| D06 | id -> present vs absent (identity = name only) | internal/endpoint/plan.go:317-320 (DeleteVolume), 438-441 (DeleteSnapshot); table.go ID = TrimPrefix(name) | row with Name == parent/<id> exists | (absent wrongly true) after admin `zfs rename` or `zfs promote`, the row moved: DeleteVolume/DeleteSnapshot return OK, data remains (still carrying csi_volume_name / csi_snapshot_name), CreateVolume(same name) makes a second dataset | (present wrongly true) n/a for names | silent-success / none | I | fail closed: secondary index by local csi_volume_name / csi_snapshot_name from the same scan; name absent but a row claims it -> FailedPrecondition('renamed to X'), never OK | 6 |
| D07 | committed row vs capacity request: compatible vs AlreadyExists | internal/endpoint/plan.go:292-306 | want>0 requires have>0 && have>=want; limit requires have<=limit; sparse/reserve must match refreservation>0 | returns an existing volume that does not satisfy the request (silent) | adopted unlimited filesystem (refquota 0 = unlimited) with any required size -> AlreadyExists (final); thick zvol whose refreservation an admin cleared -> AlreadyExists | silent-success / stuck | II (edge) | keep erring II for real mismatches (visible beats silent); fix the predicate: have==0 on a filesystem is unlimited, which satisfies required unless a limit is set | 6 |
| D14 | id -> parseable vs NotFound vs InvalidArgument | internal/endpoint/endpoint.go:154-174 (volumeID, parseSnapshotID), 341, 363, 387, 497-499; plan.go:177-180, 202-205, 381 | volumeID: non-empty, <=128 bytes, no '/' or '@', valid zfs component | n/a (a malformed id is certainly not one this endpoint issued) | malformed id treated as a client error instead of 'does not exist': DeleteVolume InvalidArgument (a static PV with handle 'tank/vols/pvc-1' or >128 bytes never deletes), Get/Validate/Expand InvalidArgument (resizer: infeasible, final) where spec says NOT_FOUND | none / stuck | II | one rule: unparseable = never issued = absent -> DeleteVolume OK, others NotFound. Empty id stays InvalidArgument (front end). Caveat: a full-path handle may name a real dataset; it is not ours, so leaving it is the closed side for data | 6 |
| D15 | reconcile -> collect vs skip | internal/endpoint/reconcile.go:130-145 (volumes), 198-229 (clone sources), 235-243 (tooYoung) | Managed && not desired && (uncommitted \|\| desired set non-empty) && age >= MinAge (default 1h) && creation known && PlanDeleteVolume allows | collects something wanted or foreign: data loss | leaves garbage: leak, collected next run | data-loss / leak | II (correct) except via D05/D10 | keep fail closed; it becomes safe only once D05 and D10 are closed | 6 |
| D04 | scan error -> absent vs unavailable | internal/endpoint/table.go:205-214 | only != '' and not-found: absent iff Exists(parent); whole-parent scan: zfsErr(err) | whole-parent scan with the parent/pool gone reports NotFound: CreateSnapshot NOT_FOUND is final for the snapshotter (VolumeSnapshot marked failed during a pool export); ListVolumes NotFound | single-volume path: parent present but get flakes -> Unavailable (retry), cheap | wrong-final / retry-storm | I (whole-parent path only) | fail toward retry: any not-found on a scan rooted at the configured parent -> Unavailable | 4 |
| D13 | apply tolerance: already-exists / already-gone = done | internal/endpoint/apply.go:34-35; plan.go:193,223 (OkExists on clone); endpoint.go:325-335 (readVolume, no Managed check) | OkExists && ErrExists -> done; OkMissing && NotFound -> done | clone target created by someone else between scan and clone is adopted: commit stamps provision_success on it, CreateVolume returns OK with foreign data; later DeleteVolume refuses (unmanaged) -> stuck | a replayed op that already succeeded fails -> retry, converges | exposure / retry-storm | I | fail closed: after an OkExists, the re-read must show Managed && VolumeName == name, else AlreadyExists | 4 |
| D03 | Exists / IsNotFound -> absent vs present | internal/zfs/errors.go:96; internal/zfs/zfs.go:151-160; apply.go:35 (OkMissing) | absent iff classified KindNotFound ('dataset does not exist', 'no such pool or dataset', 'could not find any snapshots') | absent when present: delete reports done while data remains | present when absent: delete/create errors, retried | silent-success / retry-storm | II (correct) | keep fail closed (already) | 3 |
| D12 | Kind -> endpoint/gRPC code (retry vs final) | internal/endpoint/endpoint.go:199-221 (zfsErr); server.go:34 | code by Kind only, independent of op | final code for a transient condition: Permission is classified but has no case -> Internal; ParentNotFound -> NotFound; Busy on a create/expand path -> FailedPrecondition (final) -> residue | retryable code for a permanent condition -> retry storm bounded by 1s..5m backoff | leak / retry-storm | I | key the mapping by (op, Kind): delete+Busy -> FailedPrecondition; create/snapshot/expand+Busy -> Unavailable; Permission -> FailedPrecondition with message | 3 |
| D17 | scan output -> trusted rows | internal/zfs/zfs.go:420-464 (parseColumns, duplicate (name,prop) check at 457) | each line is name\tprop\tvalue\tsource; a repeated (name, prop) rejects the scan | phantom row: a newline in a foreign user-property value forges lines for a name zfs never prints -> phantom clone blocks DeleteVolume (FailedPrecondition), phantom csi_snapshot_name blocks CreateSnapshot (AlreadyExists) | (rejects good output) n/a; the forged-duplicate case fails the whole scan -> Internal for every whole-parent RPC (DoS) | stuck / stuck | closed for collisions; open for phantoms | fail closed but scoped: reject a name whose block reappears after it closed (zfs prints each dataset contiguously), or use `zfs get -j` (>=2.3) | 3 |
| D16 | lock -> Aborted vs wait | internal/endpoint/endpoint.go:182-196; internal/lock/lock.go:26-62 | wait up to min(LockWait, deadline - 1s), then Aborted | Aborted although the lock would free soon -> CO retries (in-progress code) | waits past the caller's deadline -> DEADLINE_EXCEEDED, work continues under the lock | retry-storm / retry-storm | balanced | keep (both errors are cheap and in-progress) | 3 |
| D02 | killed / cancelled zfs call -> outcome | internal/zfs/runner.go:39-54 (CommandContext, Code -1, empty stderr) -> classify('') -> Internal; server.go:34 ctx errors -> Internal | exit != 0 with empty stderr is a classified failure | reported as a definite failure (Internal, final) although the destroy/create may have completed | n/a | wrong-final / none | I | fail toward re-scan: map deadline/cancel kill to DeadlineExceeded/Unavailable (in-progress for every sidecar) | 2 |

## Verdicts on the known inputs (checked, not trusted)

| claim | verdict | evidence |
|---|---|---|
| Unclassified stderr falls to Internal: "pool is read-only", "it's being held", "key is not loaded", "must be one of", "must be power of 2" | **Confirmed.** All five are verbatim in OpenZFS: `libzfs_util.c:258`, `libzfs_dataset.c:3921`, `:3753`, `libzfs_util.c:1941`, `libzfs_dataset.c:1058`. | Probe P7: each one → Internal. Also unclassified: "dataset is read-only", "read-only property" (the real EZFS_PROPREADONLY text; the pattern "property is read-only" only matches `zfs inherit`'s CLI line), "unsupported version or feature", and "module does not support". The pattern "bad property" matches **0** OpenZFS strings, so it is dead. |
| Busy → FailedPrecondition (final), though busy is transient | **Refined: mostly correct.** | The CSI spec requires FAILED_PRECONDITION for "in use" on DeleteVolume and DeleteSnapshot (spec.md:1340, :2345), and Kubernetes retries deletes whatever the code. Busy shows up almost only on destroy. The mapping is wrong only on create, snapshot and expand paths, which are rare. The fix is to map by (op, Kind), not by Kind alone. |
| Received props are counted as owned | **Confirmed. Data loss.** | Probe P1: a received copy reads as Managed and Committed. DeleteVolume plans `destroy -r`, and Reconcile collects the copy whenever the desired set is non-empty. |
| Rename or promote makes Delete return OK while the data remains | **Confirmed.** | Probes P2 and P3: OK with an empty plan. The moved row still carries `csi_volume_name` / `csi_snapshot_name` locally, so the endpoint could detect the move from the same scan. |
| `xattr=dir` on 2.2 reads back as `on`, giving AlreadyExists forever | **Confirmed.** | Probe P4. `zfs_prop.c` in 2.2.9 lists `on`=DIR before `dir`, while 2.4.0 maps `on` to SA. The CI noble job runs 2.2.2. |
| Clone from an unowned source is allowed | **Confirmed. Exposure, plus mutation of a foreign dataset.** | Probe P5: 4 ops; the first snapshots `foreign@volume-source-for-volume-steal`. CreateSnapshot on the same row returns NotFound, so the endpoint contradicts itself. |
| A malformed id gives InvalidArgument where the spec says OK or NotFound | **Confirmed, and inconsistent across RPCs.** | Probe P6: DeleteVolume, GetVolume, Expand and CreateSnapshot(src) return InvalidArgument; DeleteSnapshot returns OK; a CreateVolume source returns NotFound. |

New findings from this pass:
- **D04:** a whole-parent scan with the parent gone returns NotFound, which is final for the snapshotter.
- **D10:** a held foreign snapshot is the only accidental brake on `destroy -r`. lzc_destroy_snaps is all-or-nothing, so the destroy fails, but it fails as Internal and is retried forever.
- **D12:** Permission is classified but never mapped, so it becomes Internal.
- **OpenZFS bug:** `zfs_error(hdl, EBUSY, …)` at `libzfs_dataset.c:3925` passes an errno where an EZFS code belongs. `libzfs_errno()` then returns 16, not EZFS_BUSY. The stderr text is the only reliable channel, which is a further reason to classify by corpus.

## Classification coverage (measured)

Pipeline (re-runnable):
1. `python3 -I tools/extract_corpus.py <openzfs> corpus.json` pulls **811** strings mechanically out of `lib/libzfs/*.c`, `lib/libzfs/os/linux/*.c` and `cmd/zfs/zfs_main.c`:
   - 101 EZFS descriptions;
   - 432 `zfs_error_aux` texts, each paired with the EZFS code of the next `zfs_error*` call in the same function;
   - 277 CLI `fprintf(stderr)` lines;
   - 1 strerror default.

   Each string is rendered the way `zfs_verror` prints it: `"<action>: <aux or description>"`.
2. `TestCorpusDump` (copy only) sends every string through the real path: Runner → `zfs.Client` → `classify` → `zfsErr`.
3. `python3 -I tools/label_and_score.py` labels the **intended** Kind and scope:
   - The intended Kind comes from libzfs's own EZFS code. It is overridden by aux text only where the code is coarse, for example EZFS_EXISTS with "snapshot is cloned" means HasClones.
   - Scope is what `zfs create|destroy|snapshot|clone|set|get|list|inherit|promote` can print.
4. `TestCorpusRatchet` (copy only) is the registry prototype. It fails if coverage drops below the floor.

| measure | result |
|---|---|
| all 811 extracted strings, classified (Kind ≠ unknown) | **45 / 811 = 5.5%** |
| in scope (245 strings), classified | 32 / 245 = 13.1% |
| in scope, correct endpoint code (Internal counts as correct for genuine internal errors) | 111 / 245 = 45.3% |
| **in scope and decisive (intended ≠ Internal), mapped to the intended code** | **28 / 162 = 17.3%** (Kind level: 32 / 162 = 19.8%) |
| type II: decisive string left unclassified, so Internal | 130 |
| type I: decisive but wrong (ParentNotFound→NotFound ×3; Permission classified but mapped to Internal ×1) | 4 |
| decisive strings needing a Kind that does not exist yet (Held, ReadOnly, KeyUnavailable, Unsupported) | 18 |
| adding only the 5 scout strings | 28 → 33 = 20.4%. Patching one string at a time does not scale. |

Misses by intended Kind (correct / total):

| Kind | correct / total |
|---|---|
| InvalidProperty | 10/93 |
| Unavailable | 4/20 |
| Unsupported | 0/13 |
| Permission | 0/9 |
| Busy | 3/6 |
| Exists | 3/5 |
| ParentNotFound | 0/3 |
| ReadOnly | 0/2 |
| KeyUnavailable | 0/2 |
| Held | 0/1 |

The absence decision (NotFound 3/3) and the destroy blockers (HasClones 2/2, HasChildren 1/1) are fully covered. The absence predicate is the one that must fail closed, and it does.

Limitations of the measurement:
- It covers OpenZFS 2.4.99 only, so a corpus per supported version (2.2.x, 2.4.x) is still needed.
- 117 aux strings set in helper functions have no EZFS code at the call site, so they are excluded as unlabeled.
- The scope filter works at file and function level.
- The intended EZFS → Kind table holds about 45 hand rows. It is the one human judgement in the pipeline and is the thing to review.

## DOD remedy: one decision registry

Each decision is one row of data. The code reads its policy from the row, and the tests iterate over the rows. Nobody re-derives a policy at a call site.

```go
type Cost uint8 // DataLoss > Exposure > Leak = Stuck = SilentSuccess > WrongFinal > RetryStorm
type Decision struct {
	ID        string   // "D05 ownership"
	Predicate string   // human text; the Go func is looked up by ID
	Evidence  []string // OpenZFS file:line, spec.md:line, corpus ids
	CostI     Cost     // fires wrongly
	CostII    Cost     // misses wrongly
	OnUnknown Outcome  // derived: refuse/keep (closed) if CostI > CostII, else retry/allow
}
// stderr: generated, never hand-written per string
type StderrRule struct{ EZFS, Match string; Kind Kind; Src string } // from tools/extract_corpus.py at a pinned OpenZFS tag
// code: keyed by (op, Kind), not Kind: delete+Busy=FailedPrecondition, create+Busy=Unavailable
type CodeRule struct{ Op OpKind; Kind Kind; Code Code; Mutated bool /* after first mutating op */ }
```

Tests that iterate the registry:
1. **Corpus.** For each pinned OpenZFS version, every in-scope corpus row must classify to its intended Kind or appear in an explicit `acceptedUnknown` list. Coverage is printed and ratcheted.
2. **Asymmetry.** For every Decision, a fixture with uncertain input (an unknown Kind, an unknown source, an unknown version, a malformed id) must produce `OnUnknown`. One rule follows from this: if CostI > CostII, uncertainty means refuse or keep.
3. **Retry class.** For every CodeRule whose outcome may have left residue (`Mutated`), the code must be in the sidecar in-progress set.
4. **Sites.** A test greps that each `Decision.ID` is referenced at its site, so a new predicate cannot ship without a row.

Fix order by score:
1. D10: refuse destroy over foreign snapshots and holds.
2. D05: local-only ownership plus VolumeName == ID.
3. D01/D12: corpus-generated classifier and (op, Kind) codes.
4. D08: version-sourced property tables.
5. D09: require owned sources.
6. D06: lookup by `csi_*_name` as a tripwire.
7. D14: unparseable means absent.
8. D04: a missing parent on a whole-parent scan maps to Unavailable.

## Files
- `scratchpad/dod3/errors-ledger.json`: the ledger as data, including meta and coverage.
- `scratchpad/dod3/tools/{extract_corpus.py,label_and_score.py,ledger.py}`: the pipeline.
- `scratchpad/dod3/{corpus.json,classified.json,labeled.json,score.txt}`: the corpus and results.
- `scratchpad/dod3/err-copy/internal/endpoint/{type12_probe_test.go,corpus_dump_test.go}` and `err-copy/internal/zfs/corpus_ratchet_test.go`: probes and the registry prototype (copy only).
