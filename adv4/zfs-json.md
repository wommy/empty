# Can `zfs get -j` / `zfs list -j` replace `zfs get -Hp` parsing?

Scope: read-only scout. No edits to /home/user/empty. No zfs binary was run; all findings are from source.

Sources:
- Local clone: /tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/repos/openzfs (HEAD a128b8a, 2.4.99-dev, single squashed commit; sparse checkout, so files were read with `git show HEAD:<path>` and `git show zfs-2.2.9:<path>`).
- Local tags present: `zfs-2.2.9` (079ba86), `zfs-2.4.0` (7433349). Tags `zfs-2.3.0` and `zfs-2.4.4` are not local; they were read through GitHits as `github:openzfs/zfs@zfs-2.3.0` (resolved to commit bc06d816) and `github:openzfs/zfs@zfs-2.4.4` (resolved to commit 71a9f957). The commit mapping is inferred from target order; 079ba86 was confirmed against the local 2.2.9 tag.

## Version facts

1. `-j` and `--json-int`
   - zfs-2.2.9: no `zfs get -j` and no `zfs list -j`. The only JSON in that tag is `zfs channel-program` (`zfs_do_channel_program`, cmd/zfs/zfs_main.c:8155) and the `nvlist_print_json(stdout, outnvl)` call at :8293. `zfs get` (zfs_do_get, :2037) and `zfs list` (zfs_do_list, :3634) have no JSON flags.
   - zfs-2.3.0 (bc06d816): both `zfs get` and `zfs list` have `-j` and `--json-int`. Usage text at zfs_main.c:301 and :313. Option tables at :2166 (get) and :3862 (list). The `zfs_json_schema(0, 1)` call sites are at :2188, :3885, :7470, :9115.
   - zfs-2.4.0 (7433349): same flags and call sites (zfs_main.c:309, 321, 2194, 2218, 3893).
   - zfs-2.4.4 (71a9f957): same as 2.3.0, shifted by about 20 lines (zfs_main.c:310, 322, 2203, 3902).
   - So `-j` appears in 2.3.0. Whether 2.3.0 is the first release with it is not confirmed here: the 2.3.0 tag was not compared against the 2.2.x tags between 2.2.9 and 2.3.0, and the 2.2.9 check was against the tag only.

2. Output shape stability, 2.3.0 vs 2.4.4
   - Same `zfs_json_schema(0, 1)` (`output_version` with `command`, `vers_major` 0, `vers_minor` 1) in both tags (zfs_main.c:1919-1920 in 2.3.0, :1958-1959 in 2.4.4).
   - Same `datasets` object keyed by dataset name, same `fill_dataset_info` fields (`name`, `type` as FILESYSTEM/VOLUME/SNAPSHOT/POOL/BOOKMARK/UNKNOWN, `pool`, `createtxg`, plus `dataset` and `snapshot_name` for snapshots).
   - Same property builder: `zprop_nvlist_one_property` in lib/libzfs/libzfs_util.c (1417-1428 in 2.3.0, 1429-1440 in 2.4.4) produces `{"value": <string or uint64>, "source": {"type": "LOCAL|DEFAULT|INHERITED|RECEIVED|TEMPORARY|NONE", "data": ...}}`.
   - Unit of change between 2.3 and 2.4.4 is line offsets only in the grepped regions. A full diff of the two JSON regions was not performed. Stability is judged from matching constants and function bodies, not a byte diff.

## 2. String escaping

Printer: `nvlist_print_json` and `nvlist_print_json_string` in lib/libnvpair/libnvpair_json.c (HEAD lines 47-108 and 115-404). The same code is in 2.3.0 and 2.4.4.

- `"` -> `\"`, `\` -> `\\`, `\n`, `\r`, `\t`, `\f`, `\b` escaped.
- Other bytes 0x00-0x1f and code points 0x80-0xffff -> `\uXXXX`.
- Printable ASCII 0x20-0x7f written raw (DEL 0x7f included, which is legal JSON).
- Code points above 0xffff are silently dropped (neither branch emits them). This is data loss, not injection.
- Names (nvpair names, i.e. dataset names and user-property keys) go through the same string escaper.
- Invalid multibyte input: `mbrtowc` returns -1 or -2 and the function returns -1 mid-document.

Breakout: not possible. Values are nvlist strings, quotes and backslashes are escaped, and newlines become `\n`. A user-property value cannot add an object key, a line, or a dataset entry. Duplicate dataset names are impossible because the output is an nvlist keyed by name (`fnvlist_add_nvlist(d, name, item)`).

Gap: `zcmd_print_json` (lib/libzfs/libzfs_util.c:1315 in HEAD, same in 2.3.0 and 2.4.4) calls `nvlist_print_json(stdout, nvl)` and ignores the return value, then writes `\n`. So a value with invalid UTF-8 (or, under the C locale, any non-ASCII byte, since `setlocale(LC_ALL, "")` at zfs_main.c:9422 with LANG unset means the C locale and `mbrtowc` rejects bytes >= 0x80 there) leaves truncated JSON on stdout with exit code 0.

Consequence for the design: JSON output must be parsed strictly. A truncated document fails `json.Unmarshal` and must fail closed. It is also a DoS path: a non-ASCII user-property value under a parent could break every whole-parent JSON scan. Mitigation: run zfs with `LC_ALL=C.UTF-8` (the locale `mbrtowc` needs for valid UTF-8), and fall back to per-dataset queries when the whole-parent JSON fails. Whether ZFS itself rejects invalid UTF-8 in user-property values was not verified.

## 3. Partial errors

Code: cmd/zfs/zfs_main.c, zfs_do_get tail (HEAD ~2433-2437; 2.3.0 :2382-2385; 2.4.4 :2419-2422):

```
if (ret == 0 && cb.cb_json) zcmd_print_json(cb.cb_jsobj);
else if (ret != 0 && cb.cb_json) nvlist_free(cb.cb_jsobj);
```

- If `ret != 0`, the JSON object is freed and nothing is printed. The whole output is discarded for every JSON path (get, list, and the others at zfs_main.c:4037 and :9386 in HEAD).
- `ret` comes from `zfs_for_each` (cmd/zfs/zfs_iter.c, HEAD ~419-520). It collects all handles into an AVL tree, sorted by name with snapshots grouped, and only then runs the callback per node. Callback results are OR-ed into `ret`. An explicit argument that fails to open sets `ret = 1`.
- Mid-iteration vanish: the handle set is gathered first. Property values come from the handle's cached props, so a dataset destroyed after the listing does not produce a per-row error. The output reflects the listing snapshot. If a named argument vanishes before it is opened, `ret = 1`, nothing is printed, and the exit code is nonzero.
- Silent-omission caveat (code reading only): in `zfs_callback` (zfs_iter.c:80-157), a `zfs_expand_proplist` failure returns -1 before recursion, and the recursive `zfs_iter_filesystems_v2` return is discarded with `(void)`. So a child can be left out of the output with exit 0. `zfs_expand_proplist` (lib/libzfs/libzfs_dataset.c:4631) only fails on `zprop_expand_list` errors, so this is unlikely in practice, but the Go side should not rely on exit 0 alone to prove completeness.
- Exit code: 0 on success; 1 on the `ret = 1` paths. Nothing is printed on any nonzero exit in JSON mode.

## 4. Size and speed vs `-Hp`

Not measured (no zfs binary here). Estimate from the format:

- `-Hp` writes the dataset name once per property line: roughly `len(name) + len(prop) + len(value) + len(source) + 4` bytes per line.
- `-j` writes the name once per dataset, then per property about `"prop":{"value":"V","source":{"type":"LOCAL","data":"-"}}`, which is about 50-70 bytes of fixed overhead plus the prop name and value. Fixed dataset metadata (`name`, `type`, `pool`, `createtxg`) adds about 80-120 bytes per dataset.
- For a parent with 10 user props of short values and long dataset names, JSON is roughly 1.0-1.5x the `-Hp` size. It is not dramatically larger or smaller.
- CPU: the time is dominated by ioctls and property lookup, which are shared by both modes. JSON adds nvlist allocation and a per-byte `mbrtowc` loop, which is small in comparison. `--json-int` adds an `sscanf` per numeric value. Expect the same order of wall time. Measure before relying on this.

## `--json-int` caution

`zprop_nvlist_one_property` (libzfs_util.c, 2.3.0:1417-1428) converts any all-digit value into a uint64 with `sscanf("%lld")` when `--json-int` is set. Consequences for user properties:
- Leading zeros are lost ("007" becomes 7).
- Values over the signed 64-bit range wrap or are undefined (`sscanf` into a uint64 with `%lld`).
- An empty value passes the `all_numeric` test (`strspn("", digits) == strlen("")`) and then `sscanf` never writes `val`, so the result is uninitialized. Whether an empty user-property value can be set was not verified.

`--json-int` also forces `-p`. Recommendation: do not use `--json-int`. Use `zfs get -j -p`, keep values as strings, and parse numbers in Go.

## 5. Design

Text-level facts needed for the `-Hp` fallback:
- Output order is deterministic: datasets are sorted by name through `zfs_compare` (zfs_iter.c:257-315, strcmp on the name with snapshots after their dataset). Each dataset's properties are printed contiguously, in `-o` order. This is an implementation property, not a documented contract.
- Text mode prints exactly one line per requested property per dataset, with no `-s` filter. Lines are skipped only when `(sourcetype & cb_sources) == 0` (libzfs_util.c:1561, which `-s` controls) or for `-o all` props that are missing (`pl_all`). A missing user prop without `all` prints `-`. Built-in props that are not valid for datasets print "No such property" to stderr and emit no line, so the expected count must come from the request and not be assumed.

Recommendation:
- Require the `-j` path on OpenZFS >= 2.3.0, detected with a probe (`zfs version`, or try `zfs get -j -H name,type <root>` once). Parse with strict `encoding/json`, reject any exit code != 0 or parse error, use `-p` and not `--json-int`, and set `LC_ALL=C.UTF-8`.
- On 2.2.x (and any probe failure), do not scan a whole parent with `-Hp`. Query per dataset (`zfs get -Hp -o name,property,value,source <props> <dataset>`), and accept the block only if it has exactly N lines, every line's name field equals the queried dataset, and the property order matches the request. Any extra line from an injected newline makes the count exceed N, so injection is always detected and never produces a phantom row. The blast radius is then one dataset, not the whole parent.
- Cross-check the names from `zfs list -H -o name -r <root>` against the per-dataset queries. Dataset names are restricted by the repo's `datasetRE` (no newline, no space). The zfs-side name character set was not re-verified in this pass.
- For delete and snapshot planning, avoid user properties in whole-parent scans on 2.2 where possible: fetch only the built-in properties needed for planning and fetch user properties per dataset.

Note on the stated threat: count checks and exact-sequence checks stop phantom rows, but a poisoned value can still make its own dataset's query fail. That is a per-dataset denial, not a whole-parent one.
