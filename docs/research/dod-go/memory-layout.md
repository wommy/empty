# Memory layout and GC: ZFS endpoint Table

## FINDINGS

1. **Layout is not the cost.** `Row` is 216 B, 10 string fields, no pointer-free part (`internal/endpoint/table.go:14-35`). Scratch benchmark (mirror structs, Go 1.24.7, Xeon 2.8 GHz): full 10^4-row filter on `Origin` costs 19 µs/op AoS vs 7 µs/op SoA, both 0 allocs. SoA saves about 12 µs per scan.
2. **Decode dominates.** `parseGet` builds a `map[string]Prop` per dataset (`internal/zfs/zfs.go:165-181`). Mirror of that path at 10^4 datasets x 16 props: about 63 ms/op, 41 MB, 230k allocs. Spawning a trivial process: 1.5 ms/op. No `zfs` binary on this host, so zfs's own cost is unmeasured.
3. **Scans multiply.** `e.scan` runs up to twice per request; `CreateSnapshot` scans `""`, then the source volume (`internal/endpoint/endpoint.go:438`, `:450`).
4. **GC.** Every `Row` holds pointers, so the pointer bitmap covers the whole struct (`$GOROOT/src/runtime/mbitmap.go:10-12`). The pointer-free skip never applies. `Table.Rows []Row` (values, contiguous) is the right shape; `idx map[string]int` holds no pointers to rows.
5. **fieldalignment** reorders to minimize size and `ptrdata`, pointer-free fields first (`golang.org/x/tools/go/analysis/passes/fieldalignment/fieldalignment.go:160-214`). A hand reorder I measured is 208 B, saving 8 B/row (about 80 KB at 10^4). The analyzer itself was not run: unverified.
6. **unique.Make** (stdlib `unique/handle.go:36`, `:146-150`): a global weak-pointer map with background cleanup. Kubernetes uses it for DRA `UniqueString` (`kubernetes@60a317ea staging/src/k8s.io/dynamic-resource-allocation/api/uniquestring.go:26-40`).
7. **Prometheus** does not intern through `unique`. `stringlabels` packs a label set into one flat string (`model/labels/labels_stringlabels.go:26-31`). `dedupelabels` uses a mutex-guarded `SymbolTable` (`labels_dedupelabels.go:44-53`).
8. **Interning does not fit Row.** Name, ID, Mount, VolumeName and SourceID are unique per dataset. Low-cardinality columns (Type, SourceType, Origin) save a few hundred KB at most at 10^4 rows, and each handle costs a weak-pointer allocation.
9. **Retention (unverified at runtime).** `parseGet` takes substrings of one `string(out)` (`zfs.go:165`, `:181`), so every Row pins the whole zfs stdout until the Table is dropped.

## RECOMMENDATION

- **Keep:** `[]Row` values, `idx map[string]int`, `Row` as a plain value type. Do not introduce SoA, pointers to rows, or `unique.Make`.
- **Change, if measured worthwhile:** decode directly into `Row` in the scan path instead of building `Dataset.Props` maps (`zfs.go:165-181`). Keep `Dataset` for tests if needed. This is the only allocation hotspot found.
- **Change, cheap:** remove the second full scan in `CreateSnapshot` by deriving the new row from the first table where possible.
- **Change, cheap:** apply the fieldalignment fix to Row (216 to 208 B) and `strings.Clone` the parsed fields if peak memory matters (item 9).

## NOT WORTH IT

SoA columns, `unique.Make` for repeated names, label interning, and any layout change in front of an exec of `zfs`. At 10^4 rows the layout cost is roughly 0.02% of a 63 ms decode.

## BENCHMARK THAT DECIDES IT

- `internal/zfs`: `BenchmarkParseGet` over a captured `zfs get -Hp -o name,property,value,source` fixture at 10^3 and 10^4 datasets, with `b.ReportAllocs()`. Compare current `parseGet`+`decode` against direct decode into `Row`.
- `internal/endpoint`: `BenchmarkSnapshotsOf` / `BenchmarkClonesOf` over a 10^4-row `NewTable`, with `b.ReportAllocs()`.
- Decision rule: change decode only if parse plus decode exceeds 10% of end-to-end request time, measured with a fake `Runner` replaying captured output.

Scratch code: `/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/align/dod-go/bench/layout_test.go`

## RISKS

- Numbers come from mirror structs on one host, not from the repo, and no zfs was available to time the exec.
- Item 9 (substring retention) is read from code, not measured.
- Whether `zfs list -o` can return property *source* (needed by `Local()`) is unverified. Check `openzfs/man/man8/zfs-list.8` before any switch away from `zfs get`.
