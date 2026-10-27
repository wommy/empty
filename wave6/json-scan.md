# Replacing the `zfs get -Hp` scan with `zfs get -j -p`: a prototype

Scope: a prototype in a copy of HEAD `0c884df` at `scratchpad/wave6/json-copy`. The repo was not edited. Toolchain: `GOTOOLCHAIN=go1.27.3`. The OpenZFS citations are from the `zfs-2.4.0` tag in `scratchpad/repos/openzfs`, extracted to `scratchpad/{zfs_main,libzfs_util,libnvpair_json,libzfs_dataset}-2.4.0.c`.

**Recommendation:** decode with **`encoding/json/jsontext` token streaming** (Go 1.27 ships `encoding/json/v2` and `jsontext` without GOEXPERIMENT) into the existing `Columns`. Run it behind a new optional `Streamer` runner seam, and do all strictness checks by hand.
- Do not use v1 `Unmarshal`. It silently accepts duplicate keys, matches member names case-insensitively, and replaces invalid UTF-8.
- Do not use v1 `Token`. It boxes every token: 48M allocations at 110k, and it also accepts invalid UTF-8.
- v2 `Unmarshal` into a typed struct is strict enough, but it costs 16M allocations and 3× the CPU at 110k, and the map loses zfs's order.

## Files (all in `json-copy/internal/zfs`)

| file | what |
|---|---|
| `json.go` | `decodeGetJSON(io.Reader, props) (*Columns, error)`: the strict decoder |
| `runner.go` | `Streamer` interface plus `LocalRunner.Stream` (stdout pipe, `LC_ALL=C.UTF-8`, drain, exit status wins) |
| `zfs.go` | `ListColumns` switched to `get -j -p`; `Client.stream` falls back to `Run` + `bytes.Reader` for runners without `Stream` |
| `zfstest/json.go` | `AppendGetJSON`: a byte-exact model of `nvlist_print_json` under C.UTF-8, including its faults |
| `zfstest/sim.go` | `get -j` emits that JSON; lazy `createtxg` |
| `testdata/getjson/*.json` | four goldens written by hand from the C source, plus `basic.txt` (the same scan in `-Hp` form) |
| `json_test.go` | goldens, strictness table (44 mutations), forgery, truncation at every offset, real-process exit tests, fuzz |
| `json_bench_test.go` | the three alternative decoders, equivalence on the dod3 fixture, benchmarks, retained heap |
| `export_test.go` | exposes `decodeGetJSON` and `parseColumns` to `zfs_test` |

Results:
- `go test ./internal/...` passes in the copy. That includes endpoint, reconcile-fuzz seeds and CSI sanity, all now running the JSON decoder through the Sim.
- `FuzzDecodeGetJSON` ran 3 min (727k execs) on the first version and 2 min (480k execs) on the final one, with no failures.

## The JSON shape, derived from source

```
{"output_version":{"command":"zfs get","vers_major":0,"vers_minor":1},
 "datasets":{"<name>":{"name":"<name>","type":"FILESYSTEM|VOLUME|SNAPSHOT","pool":"<pool>","createtxg":"<n>",
                       ["dataset":"<ds>","snapshot_name":"<snap>",]      // snapshots only
                       "properties":{"<prop>":{"value":"<v>","source":{"type":"NONE|DEFAULT|LOCAL|TEMPORARY|INHERITED|RECEIVED","data":"-|<from>"}}, ...}}, ...}}\n
```

Where each part comes from:
- **`zfs_json_schema(0,1)`** (`zfs_main.c:1939`): `command` is `"zfs " + current_command->name`.
- **`get_callback`** (`:2031`) and **`fill_dataset_info`** (`:1957`).
- **`zprop_nvlist_one_property`** (`libzfs_util.c:1423`).
- **The printer, `nvlist_print_json`** (`libnvpair_json.c`):
  - It writes no whitespace. `zcmd_print_json` (`libzfs_util.c:1285`) adds one `\n` and ignores the printer's return value.
- **Order:**
  - The top-level, dataset and property objects are all `fnvlist_alloc` (NV_UNIQUE_NAME), so members appear in insertion order.
  - Datasets come in `zfs_for_each` / `zfs_compare` order, and properties in `-o` request order.
- **Escaping:**
  - `" \ \n \r \t \f \b` use short escapes.
  - Other characters below 0x20, and 0x80–0xFFFF, become `\u%04x` in lower-case hex.
  - 0x20–0x7F is written raw, including DEL.
  - The output is therefore pure ASCII.

Source mapping to the text spelling, which keeps `Columns` identical to `parseColumns`. This follows `zprop_print_one_property`, `libzfs_util.c:1520`:

| JSON source | `-Hp` source |
|---|---|
| `NONE`, `-` | `-` |
| `DEFAULT`, `-` | `default` |
| `LOCAL`, `-` | `local` |
| `TEMPORARY`, `-` | `temporary` |
| `RECEIVED`, `-` | `received` |
| `INHERITED`, `X` | `inherited from X` |

### What surprised me

1. **Code points above U+FFFF are silently dropped.**
   - The printer has a branch for `<=0x1f`, `0x80..0xffff` and `0x20..0x7f`, and nothing else.
   - So `"a😀b"` reads back as `"ab"`: the JSON is valid, zfs exits 0, and the value is wrong. No decoder can detect this.
   - It is harmless for our own properties if `checkProps` restricts what we write to printable ASCII (recommended below). Foreign values are lossy.
2. **Invalid UTF-8 truncates the document mid-string, then prints `\n`, and zfs exits 0.**
   - This was already known. What follows from the source: the closing `}` of the top-level object is written only at the very end, so any truncation is a JSON syntax error.
   - Fail-closed is guaranteed by the grammar, not by luck.
   - The decoder's error names the poisoned dataset and property: `json scan at byte 255: dataset "tank/x": property "p:note": jsontext: invalid character '\n' in string ...`.
3. **`createtxg` is conditional** (`if (zfs_prop_get(...CREATETXG...) == 0)`), and it is a *string* unless `--json-int` is given. `vers_major` and `vers_minor` are the only JSON numbers.
   - The decoder requires `createtxg`. It always resolves for datasets, but this is the one place where strictness is stricter than the source.
4. **A dataset whose property nvlist ends up empty is omitted entirely** (`if (!nvlist_empty(props))`). That happens with `-s` filtering, or when every requested property hits the "No such property" `continue`.
   - In text mode it is simply zero lines, so `parseColumns` never noticed either.
   - In JSON a *partly* missing property list is detectable, and the decoder errors on it. `parseColumns` silently fills in `"-"`.
5. **Text mode truncates `inherited from %s` at 128 bytes** (`char buf[128]` in `zprop_print_one_property`). JSON carries the full source name.
   - So `Columns` equality with `parseColumns` holds only for inheriting dataset names shorter than 114 bytes.
   - JSON is the correct one. Nothing downstream parses the inherited-from name (`Prop.Local` checks only `local` and `received`).
6. **`RECEIVED.data` is the received value only when `-o received` is requested.** Otherwise it is `"-"`. The decoder requires `"-"` for every non-INHERITED source.
7. **Duplicates cannot come from zfs.** Every object is NV_UNIQUE_NAME, so a second add *replaces*. A duplicate in the stream therefore means forgery or corruption, and it is rejected.
8. **The `-o` columns and `-H` have no effect on `-j` output.** The new argv drops them: `zfs get -j -p -t … -d N props root`.
9. **Unknown property names, and aliases such as `compress` or `recsize`:** zfs keys them by their canonical name, so a request that uses an alias now fails loudly. `parseColumns` used to `continue` and leave `"-"`.

## Decoder contract (`decodeGetJSON`)

Nothing is returned unless all of the following hold:

- **Document:**
  - Exactly one top-level object holding `output_version` and `datasets`, each exactly once, and nothing else.
  - Then only whitespace, then EOF. A second document, trailing garbage or `{}` is an error.
- **`output_version`:** exactly `{"command":"zfs get","vers_major":0,"vers_minor":1}`. A future 0.2 fails closed, deliberately, per ADR 0004.
- **Each dataset:**
  - `name` equals its key.
  - `pool` equals the first component.
  - `type` is one of FILESYSTEM, VOLUME or SNAPSHOT.
  - `createtxg` is all digits.
  - `dataset` and `snapshot_name` appear exactly when the name has `@`, and they match the name.
  - If `type` was requested, its value agrees with the dataset type.
- **Properties:** every requested property appears exactly once, nothing else appears, and each is exactly `{"value": string, "source": {"type", "data"}}`.
- **Repeats:** no member repeats at any level, and no dataset key repeats.
- **Strings:**
  - Invalid UTF-8 and lone-surrogate escapes are rejected (jsontext's default).
  - Member names are case-sensitive.
  - Values must be strings, so `--json-int` output is rejected.
- **The request itself:** a property list containing `all`, an empty name or a duplicate is refused.
- **Process:** the exit status is checked by `LocalRunner.Stream`. A non-zero exit wins over any decode result, even if a complete document was read. A decode error with exit 0 is an error.
  - On a decode error stdout is drained, so zfs can exit and report its status, and no partial `Columns` escape.

Implementation notes:
- `ReadValue` returns bytes that are only valid until the **next read**. The first cut switched on a member name after reading its value. That passed the small goldens and broke at the 8 KiB buffer refill in the endpoint suite. The final code classifies the name before the next read.
- Retained strings go into a 256 KiB-chunk arena via `unsafe.String`, and `"-"` is interned. That gives 150 allocations at 10k and about 830 at 110k.
- Two profile-driven fixes:
  - Doubling `Cells` instead of append's 1.25× growth: growslice was 9% of CPU.
  - `jsontext.AllowDuplicateNames(true)` plus hand checks (bitmasks, `seen[]`, and a name set for dataset keys) in place of jsontext's per-object namespace: `objectNamespace.insert` was 13% of CPU.
  - Before the fixes, 10k cost 55.7 MB and 10,263 allocs/op. After them: 21.7 MB and 150.

## Benchmarks

Input:
- `dod3/fixture.{10k,110k}`: 27 columns, democratic-csi names, OpenZFS order, the same data as dod3. 10k is 10,001 datasets and 110k is 110,001.
- Converted to JSON with the Sim's printer: 24.9 MB at 10k and about 275 MB at 110k, against 31.5 and 347.5 MB of text. JSON is *smaller*, because the name is not repeated on every line.

Machine and noise:
- 4 vCPU Xeon @ 2.8 GHz.
- **The host was shared with other agents' test runs, at load average 37–52.** ns/op is wall time and noisy, so ranges are min–max over 3–9 runs, and comparisons should use the minimums.
- B/op and allocs/op are deterministic.

In memory, bytes already produced (`bytes.Reader` / `[]byte`):

| decoder | 10k ns/op | 10k B/op | 10k allocs | 110k ns/op | 110k B/op | 110k allocs | strict enough? |
|---|---|---|---|---|---|---|---|
| `-Hp` `parseColumns` (today) | 133–237 ms | 96.0 MB | 270,187 | 2.10–3.23 s | 1.10 GB | 2,970,7xx | no (phantom datasets, F5) |
| **jsontext stream (chosen)** | **200–335 ms** | **21.7 MB** | **150** | **2.62–4.74 s** | **296 MB** | **~830** | **yes** |
| v2 `UnmarshalRead`, typed struct | 651–1167 ms | 57.9 MB | 1,465,163 | 7.7–9.5 s | 628 MB | 16.1M | yes, but order lost |
| v1 `Decoder.Token` | 680–958 ms | 104.9 MB | 4,371,090 | 5.6–10.4 s | 1.19 GB | 48.1M | no (accepts invalid UTF-8) |
| v1 `Unmarshal`, typed struct | 793–1365 ms | 125.0 MB | 1,465,171 | 7.2–13.9 s | 1.70 GB | 16.1M | no (duplicates, case folding, invalid UTF-8) |

Through a real pipe (`LocalRunner` + `cat` standing in for zfs; this is what production pays):

| | 10k | 110k |
|---|---|---|
| `Run` + `parseColumns` (today) | 282–453 ms, 163 MB, 270k allocs | 2.65–4.41 s, **2.17 GB**, 2.97M allocs |
| `Stream` + jsontext | 204–271 ms, **21.7 MB**, 261 allocs | 2.21–3.65 s, **296 MB**, 942 allocs |

Retained heap of one result, which the Table keeps alive:

| | 10k | 110k |
|---|---|---|
| `parseColumns` | 38.9 MiB | 434 MiB (every cell is a substring of `string(out)`, so the whole stdout stays live) |
| jsontext | 13.2 MiB | 177 MiB |
| v2 Unmarshal | 12.7 MiB | 140 MiB (exact-size Cells) |

Reading the numbers:
- On CPU alone, jsontext is about 1.2–1.5× the *current* text parser on the in-memory path. Through a real pipe it is equal or faster, because it drops the `bytes.Buffer` doubling.
- It allocates 4–7× fewer bytes and about 3,500× fewer objects, which matters most for the concurrent-scan GC collapse in dod3 F3.
- It cannot match dod3's tuned positional text parser (0.43 s at 110k): jsontext validates every byte at about 75–105 MB/s here. That gap is the price of a grammar that forgery cannot break, and it is small next to the zfs-side kernel walk.
- If it ever matters, a hand-written scanner for this fixed grammar is possible. I would not write one; jsontext is the reviewed parser.

`encoding/json/v2` / `jsontext` under 1.27.3: `go doc encoding/json/v2` and `go doc encoding/json/jsontext` resolve, and both compile without GOEXPERIMENT. They are better here for two reasons:
- **Syntax:** strict by default (duplicates, UTF-8, case-sensitive names).
- **Speed:** `jsontext.Decoder.ReadValue` returns raw bytes with no boxing.

## Tests

- **Goldens** (`testdata/getjson/`):
  - `basic.json` is four datasets: a filesystem, a volume, a snapshot with INHERITED user properties, and a clone. It decodes **DeepEqual** to `parseColumns(basic.txt)`.
  - `escapes.json` holds a value with `\n \t \" \\ / é 中 😀 \x01 DEL \r \f \b` (the 😀 is dropped by zfs), a dataset named with a space, and both a `-Hp` forgery and a JSON-structure forgery as values.
  - `invalid-utf8.json`: the truncated document zfs prints with exit 0. It fails.
  - `empty.json`: datasets `{}`, which gives zero rows.
  - `TestSimPrinterMatchesGoldens` checks that the Sim's printer reproduces all four byte for byte.
- **Forgery** (`TestNewlineForgeryImpossible`): a foreign writer sets `p:name` to a value carrying three forged `-Hp` lines for `tank/k/phantom`.
  - The test first proves the hole: `parseColumns` on the Sim's `-Hp` output yields a phantom dataset with `p:managed=true local`.
  - Through `ListColumns` (JSON), the names are exactly the real datasets, and the forged text is that property's value verbatim. The same holds for a value written as JSON syntax, `x"}}}},"tank/k/phantom":{...`.
- **Truncation:** for the basic, escapes, empty and Sim-generated documents, every prefix `[:i]` for i < len, both with and without zfs's trailing `\n`, fails. The whole document with or without the `\n` decodes.
- **Strictness:** 44 mutations, each rejected:
  - duplicate dataset key, property, member, value, source, source.type, source.data, vers_minor, datasets and output_version;
  - unknown members at every level;
  - case-folded names;
  - missing createtxg or properties;
  - extra property;
  - numeric createtxg, `--json-int` value, null;
  - name, pool, type or snapshot-field mismatches;
  - BOOKMARK;
  - version 0.2 or `1.0e0`;
  - another command;
  - trailing garbage, second document;
  - raw invalid UTF-8, `\ud800`;
  - INHERITED without data, LOCAL with data, unknown source type;
  - empty input, whitespace only, array in place of an object.
- **Exit status** (a real process, via shell scripts as `Binary`):
  - A full valid document followed by `exit 1` is an error classified NotFound from stderr.
  - 4 MB of garbage with exit 0 gives a decode error, and the child is reaped.
  - The truncated document with exit 0 fails.
  - The `Run` fallback classifies too.
- **Fuzz** (`FuzzDecodeGetJSON`, over raw bytes and three property sets):
  - No panic.
  - On success, `Names` equals the `datasets` keys extracted by an independent v1-token walk, in order, and the input must be valid to v1.
  - Names are unique, `Cells` is `len(Names)*len(props)`, and no source is empty.

## Migration sketch

**`runner.go`** (+50 lines):

```go
+type Streamer interface {
+	Stream(ctx context.Context, name string, args []string, read func(io.Reader) error) error
+}
+func (LocalRunner) Stream(...) error {
+	cmd.Env = append(os.Environ(), "LC_ALL=C.UTF-8")   // C locale: mbrtowc rejects every byte >= 0x80 → truncated JSON, exit 0
+	stdout, _ := cmd.StdoutPipe(); cmd.Start()
+	rerr := read(stdout)
+	if rerr != nil { io.Copy(io.Discard, stdout) }      // let zfs exit and report its status
+	if err := cmd.Wait(); err != nil { return &ExitError{...stderr...} }   // non-zero exit wins
+	return rerr
+}
```

**`zfs.go`** (`ListColumns`):

```go
-	args := []string{"get", "-Hp", "-o", "name,property,value,source", "-t", strings.Join(ts, ",")}
+	args := []string{"get", "-j", "-p", "-t", strings.Join(ts, ",")}
 	...
-	out, err := c.run(ctx, "list", root, args...)
-	...
-	cols, err := parseColumns(out, o.Props)
+	err := c.stream(ctx, "list", root, func(r io.Reader) (err error) {
+		cols, err = decodeGetJSON(r, o.Props)
+		return err
+	}, args...)
+// stream: Streamer if the Runner has it, else Run + bytes.NewReader; ExitError → classify(stderr), else KindUnknown
```

**`zfstest/sim.go`:**
- Accept `-j`, `-p` and `-H` as separate flags.
- Collect `JSONDataset`s and render them with `AppendGetJSON` when `-j` is set.
- Add a `txg` field, assigned lazily.

The Sim does not implement `Streamer`, so it goes through the fallback. Contract tests therefore exercise `decodeGetJSON` on the exact bytes zfs would print.

The full diffs are in `scratchpad/wave6/{zfs.go,runner.go,sim.go}.diff`.

**Deletable once this lands:**
- `parseColumns`, about 45 lines including the `seen[]` newline fix.
- `FuzzParseColumns` and `TestParseColumnsRejectsForgedLines` in `fuzz_test.go`.
- The `"-"` fill-in for missing cells, which is gone with `parseColumns`.
- `Columns`, `Cell` and `Col` stay, as the Table's input.

**Stays for now:** `parseGet`, used by `Get` and `List`.
- `Get(..., "all")` has an open property set, which the strict decoder refuses by design.
- Migrate `Get` to JSON with a non-strict "any properties" mode, or keep it per-dataset on `-Hp`. A forged line there can only name the queried dataset, and `Get` already looks up `ds[name]`, but a forged *property* line could still overwrite a value. So `Get` should move to JSON too, with `props` explicit at every call site except `Get(name)` with no properties.

**Also worth doing alongside:**
- `checkProps` should refuse non-printable-ASCII values (it refuses only `\n \t \0` today). Every value we write then round-trips exactly through the JSON printer, which is not true for astral characters.
- `Table.decodeColumns`'s missing-column bug (dod3 F6, `c.Cell(i, -1)`) is **not** fixed by this. The strict decoder guarantees every *requested* column is present, but `decodeColumns` still reads columns by name that a narrowed request may not include. Make `cell(-1)` return `{"-","-"}`.

## Risks

1. **Fail-closed DoS.** One foreign dataset under the parent with an invalid-UTF-8 user-property value (or, if `C.UTF-8` is missing on the host, any non-ASCII value) breaks every full scan, because zfs truncates with exit 0.
   - The error names the dataset and property, so an operator can fix it.
   - Narrowing the requested columns (dod3 F6) shrinks the exposure to the properties we actually read.
   - Not verified: whether `zfs set` rejects invalid UTF-8 in user-property values. Check this on the 2.4 VM.
2. **`LC_ALL=C.UTF-8` must exist.** glibc 2.35+ has it built in, which covers Ubuntu 26.10 and TrueNAS 27. Add a CI assertion on the VM: `LC_ALL=C.UTF-8 zfs get -j -p name <pool>` with a non-ASCII user property.
3. **Version pinning.** `vers_minor != 1` fails every scan after a future OpenZFS bump that changes the schema. That is intended by ADR 0004, but it is a hard outage on upgrade. The CI gate on the 2.4 VM must run the real decoder.
4. **The goldens are derived from source, not captured.** No zfs binary was run here. The first real-pool CI run should capture `zfs get -j -p` output into `testdata` and diff it against the Sim's printer. The byte-exact Sim-vs-golden test makes any drift visible.
5. **`unsafe.String` arena:** private to the package, and chunks are never rewritten. Strings keep their 256 KiB chunks alive for as long as the Table lives, the same lifetime as today at about 40% of the bytes.
6. **Doubling `Cells`** can leave up to 2× capacity live (177 MiB against v2's 140 MiB at 110k). A final `slices.Clip` plus copy is not worth 95 MB of copying. A capacity hint from the previous scan would remove it.
7. **CPU:** about 1.2–1.5× the current text parser in memory, and break-even or better through a pipe. dod3's narrowing (F6) and subtree scans (F2) are larger levers than the parser.
