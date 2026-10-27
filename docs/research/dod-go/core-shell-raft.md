# Functional core / imperative shell: etcd raft, endpoint plan/apply

Refs: etcd-io/raft@638f4273 (node.go, rawnode.go, raft.go); doc.go is at @c51f010c (ref mismatch, same design).

## FINDINGS

**1. etcd raft as the canonical example**
- `Config.Applied` is the restart cursor; raft never returns entries at or below it (raft.go:147-151).
- `raft.Step(m)` is the transition: it takes a message and mutates state (raft.go:1089). It does no I/O, and time enters only through the caller's Tick (node.go:458). That is why the core is deterministic and testable by feeding messages.
- `Ready` is a read-only batch: SoftState, HardState, Entries to persist, CommittedEntries to apply, Messages to send, and Snapshot (node.go:52-110). The comments say persist before send (node.go:58-60, 100-101).
- `RawNode.Ready()` calls `readyWithoutAccept` then `acceptReady` (rawnode.go:131-135). `acceptReady` clears `msgs` and records the pending acks in `stepsOnAdvance` (rawnode.go:400-429). It panics if a second Ready is accepted before `Advance` (rawnode.go:411-413).
- `Advance` replays `stepsOnAdvance` through `Step` (rawnode.go:477-489). `node.Advance` is a channel send (node.go:555-560).
- **Batching:** the run loop rebuilds Ready only when no Advance is pending (node.go:353-365). Everything that accumulated since the last Advance goes out as one Ready. The comment says this is deliberate: larger Readys, fewer and more predictable emissions (node.go:358-362).
- **Applied:** the applied index advances when a Ready is accepted (rawnode.go:431-434). In sync mode, calling Advance is the application's acknowledgement. There is no per-entry callback. The doc's rule is that Readys are processed in order and Advance comes after apply (doc.go:101-103, @c51f010c).

**2. Mapping to endpoint**
- `Plan` (`[]Op`, plan.go) already is a per-request batch. `apply` runs it in order and returns only the first error (apply.go:9-45). It reports no progress.
- The re-read after apply (endpoint.go:287-290) is the real "applied" signal.
- Cross-request batching would couple per-volume locks (endpoint.go:172-181, 273) and failure domains, and the zfs CLI gives no multi-dataset atomicity. Batch the scan instead: `scanFor` is already one call per volume (endpoint.go:300+).
- Apply should return per-op outcomes (`Done` / `Tolerated`) plus the failing index, not write into `Table`. The Table is a scan snapshot, and the re-read stays the source of truth.

**3. Ops as data**
- The current `Op` is a fat struct. Several fields are valid only for some kinds: `Size`/`Sparse` (CreateVol), `Recursive`/`Defer` (Destroy), `Source` (Clone), `OkMissing` (Destroy) (plan.go, Op struct). The type cannot express that.
- Go's own sum pattern is a sealed interface with an unexported method: go/ast `exprNode()` (go/ast/ast.go:44, 573-575).
- Exhaustiveness: `exhaustive` checks enum switches by default; typeswitch is opt-in (`-check=typeswitch`, exhaustive.go:45, 58). A default case is not treated as exhaustive unless `-d` is set (exhaustive.go:42; check.go:61-64).
- Precedents: CockroachDB's `RequestUnion` is a protobuf oneof and `BatchRequest` is a flat slice of it with flag-derived predicates (pkg/kv/kvpb/api.proto:2531-2545; batch.go:200-201). Kubernetes uses a string-typed enum for patch kind (apimachinery pkg/types/patch.go:22-31).

## RECOMMENDATION

- Keep `Plan` as `[]Op` (the batch shape is right) and do not batch across requests.
- Change `Op` to a sealed interface, one struct per kind (`createFS`, `createVol`, `snapshot`, `clone`, `set`, `destroy`). Each carries its own tolerance (`okExists` / `okMissing`). Use pointer receivers or `cmp` in tests, since `==` on structs with maps panics through an interface.
- `apply` gets a type switch with `default: return internal error`. Lint with `exhaustive -check=switch,typeswitch -default-signifies-exhaustive=false`.
- `apply` returns `([]Outcome, error)`, with the error wrapping the failing op index.
- Go 1.24 compatible: nothing new is used. Experimental packages: none.

## RISKS

- Churn in `plan_test.go` and `endpoint_test.go` if they inspect `Op` fields directly (not checked).
- Failure-path behavior must stay identical. The tolerance flags move from a field to a type, so tests must cover each op's replay.
- Unverified: whether `zfs` offers any multi-dataset atomicity beyond `lzc_destroy_snaps` (not checked).
- Not checked: the local OpenZFS checkout is unused here; the question is Go-design, not ZFS behavior.
