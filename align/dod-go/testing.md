# Testing the data-oriented core (Go 1.24.7)

## FINDINGS

- **Convergence bug (probe-verified, scratch copy).** `PlanCreateVolume` sets `refreservation` only inside `if want > 0` (internal/endpoint/plan.go:195). A filesystem with `Reserve: true` and no capacity commits with `Refres == 0`; the retry fails `compatible` (plan.go:241) with AlreadyExists. Violates `Plan(simulate(t,p),r)` empty.
- **Fuzz smoke runs (15s each, scratch copy).** `FuzzParseGet` (internal/zfs/zfs.go:163) and `FuzzClassify` (internal/zfs/errors.go:134) found nothing. Their invariants were weak.
- **Stderr precedence.** `classify` is ordered: `"no such pool or dataset"` (errors.go:118, NotFound) precedes `"no such pool"` (errors.go:129, Unavailable). OpenZFS emits the generic text from libzfs_util.c:103, and the explicit missing-pool text from libzfs_pool.c:1379. Judgment call, unverified against a live pool.
- **rapid** (pgregory.net/rapid, Context7 docs): StateMachine is `Check(*T)` plus `ActionName(*rapid.T)` methods, at least one required; `rapid.StateMachineActions(sm)` feeds `T.Repeat` (statemachine.go:71-78, :110-111, via GitHits). `SyncTest` needs Go 1.25, so avoid it. Not in go.mod; must be added.
- **testing/quick** (stdlib): `-quickchecks` flag (quick.go:20), `MaxCount` (quick.go:178-212), no shrinking.
- **Golden files:** CockroachDB datadriven uses `-rewrite` with a "verify the diffs" note (datadriven.go:36-41). zfs.propArgs sorts keys for deterministic argv (zfs.go, propArgs).

## RECOMMENDATION

1. Fix plan.go:195: set `refreservation` when `r.Reserve`, regardless of `want`. Add a regression test in plan_test.go. Not applied; endpoint is out of scope.
2. Native fuzz targets (internal test files):
   - `FuzzParseGet` (zfs): no panic; each `Dataset.Name` equals its map key; no newline in values.
   - `FuzzClassify` (zfs): `KindNotFound` implies an absence substring; a missing pool never maps to NotFound unless it says "or dataset".
   - `FuzzPlanCreate` (endpoint): bytes decode into `[]Row` via `props.decode` plus `NewTable`; request decoded from bytes. Assert no panic, `Op.Target` prefixed by `Parent+"/"`, and convergence. Seed with the plan_test.go cases.
3. Convergence as a rapid StateMachine over `zfstest.Sim`:

```go
type epSM struct {
	ep    *endpoint.Endpoint
	model map[string]endpoint.CreateVolumeRequest
}

func (m *epSM) CreateVolume(t *rapid.T) {
	req := endpoint.CreateVolumeRequest{
		Name:          rapid.StringMatching(`[a-z]{1,4}`).Draw(t, "name"),
		Kind:          rapid.SampledFrom([]endpoint.VolumeKind{endpoint.Filesystem, endpoint.Block}).Draw(t, "kind"),
		CapacityBytes: rapid.Uint64Range(0, 1<<30).Draw(t, "cap"),
		Sparse:        rapid.Bool().Draw(t, "sparse"),
	}
	if _, err := m.ep.CreateVolume(ctx, req); err == nil {
		m.model[req.Name] = req
		if _, err := m.ep.CreateVolume(ctx, req); err != nil { // replay is a no-op
			t.Fatalf("replay: %v", err)
		}
	}
}
func (m *epSM) Check(t *rapid.T) { /* scan keys == model keys; each volume compatible */ }

func TestEndpointModel(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		t.Repeat(rapid.StateMachineActions(&epSM{ /* ep over zfstest.New("tank") */ }))
	})
}
```

   Fit: good. Sim is a stateful oracle, and actions map to create, delete, expand, snapshot. Keep Sim as the oracle, not as the code under test.
4. Table-driven: keep `[]struct{name; in; want}` with `t.Run` and `t.Helper()`.
5. Printable plans: add `OpKind.String()` (none exists; golden output would print integers) and a `Plan.String()` with sorted `Props` keys. Golden files in `testdata/plan/*.golden`, updated with `-update`.

## RISKS

- rapid adds a dependency; `testing/quick` lacks shrinking.
- Committed fuzz corpora under `testdata/fuzz`; the Table decoder becomes test code that tracks `decode`.
- Sim's approximate space and reservation model can hide real zfs behavior (unverified).
- OpenZFS string references come from the local checkout, not a live pool.
