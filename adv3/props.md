# Adversarial review: property allowlist (735de85..ba26fd8^2)

The bar is OpenZFS's own behaviour: what libzfs accepts and what `zfs get -Hp` prints, on both 2.2 and 2.4. democratic-csi adoption is a secondary concern, so no finding below depends on it.
Sources: zfs-2.2.9 and zfs-2.4.0 tags, fetched shallowly into `repos/openzfs`; HEAD is 2.4.99.
Throwaway repro: `adv3/props-copy/internal/endpoint/adv_props_test.go`, with an `Emulate22` switch added to the copy's `zfstest/sim.go`.

## F1 (HIGH): on 2.2 userland, `xattr=dir` never reads back as `dir`, so every identical retry returns AlreadyExists

- **Where:** `internal/endpoint/props.go:113` maps `"dir"` to `"dir"`. The check that fails is the compare at `props.go:220`, called from `plan.go:153`.
- **Proof (OpenZFS):** in zfs-2.2.9 `module/zcommon/zfs_prop.c:363-369` the table is `{"off",OFF},{"on",ZFS_XATTR_DIR},{"sa",SA},{"dir",ZFS_XATTR_DIR}`. `zprop_index_to_string` (`module/zcommon/zprop_common.c:341-345`) returns the first name that matches. So `zfs get -Hp xattr` prints `on` for a dataset created with `-o xattr=dir`. In 2.4.0 (`zfs_prop.c:365-371`) `"on"` moved to `ZFS_XATTR_SA`, and only from then on does `dir` print as `dir`.
- **Scenario:** on 2.2 userland, CreateVolume `{xattr: dir}` creates the volume and commits. Any identical retry (for example after a provisioner timeout) reads `Local[xattr]="on"` and returns `AlreadyExists "exists without xattr=dir set locally"`. That repeats forever, so the PVC stays Pending.
- **Repro:** `go test ./internal/endpoint -run TestAdv` in the copy fails with `code=6 ... exists without xattr=dir set locally`.
- **Related semantic drift on 2.2:** `xattr=on` is canonicalised to `sa` and passed as `-o xattr=sa`. On 2.2, `on` means `dir`, so the caller silently gets a different value from the one OpenZFS would have given.
- **Minimal fix:** make the xattr canonicalisation follow the userland version. Detect it once at startup with `zfs version`, or probe by creating a throwaway dataset with `-o xattr=dir` and reading it back. Then use 2.2 = {on→on, dir→on, sa→sa, off→off} and 2.4 = {on→sa, sa→sa, dir→dir, off→off}. Pass the caller's literal value to `-o` and compare canonical forms on both sides. The cheaper alternative is to accept only `sa` and `off` for xattr.

## F2 (MEDIUM): the simulator echoes `-o` values verbatim, so the normalisation and FuzzCreateConverges' read-back check are tautological

- **Where:** `internal/zfs/zfstest/sim.go:573`, where `value()` returns `n.props[prop]` unchanged. The fuzz check is `internal/endpoint/converge_test.go:73`, which compares `sim.Prop(...)` against the canonical value.
- **Why it proves nothing:** the endpoint sends the canonical string, and the sim stores and prints that same string. The check therefore cannot catch any case where canonical ≠ what zfs prints. F1 is exactly such a case, and so is any future table change.
- **Proof (OpenZFS):** real zfs stores the numeric index and prints the first table name (`zprop_common.c:341-345`). For example, `-o compression=gzip-6` prints `gzip`, and on 2.2 `-o xattr=dir` prints `on`. The sim prints whatever it was given.
- **Minimal fix:** in the sim, map index properties through a copy of the `zfs_prop.c` tables (string → index → first name), with a version knob for 2.2 and 2.4. Have the fuzz target also run a second CreateVolume. Its doc comment claims this already, so check that it is really exercised with props, under both table versions.

## F3 (MEDIUM): `snapdir=disabled` is advertised and accepted, but 2.2 rejects it, and the error is mapped to Internal (retried forever)

- **Where:** `internal/endpoint/props.go:115`, and `api/zfsendpoint/v1/endpoint.proto` (the properties comment).
- **Proof (OpenZFS):** zfs-2.2.9 `zfs_prop.c` snapdir_table has only hidden and visible. `"disabled"` was added in 2.4 (diff of the 2.2.9 and 2.4.0 versions of `zfs_prop.c`, line 242).
- **What 2.2 prints:** `lib/libzfs/libzfs_util.c:1752-1755` in zfs-2.2.9 emits `cannot create '...': 'snapdir' must be one of 'hidden | visible'`. None of `internal/zfs/errors.go` patterns match that text, so `classify` returns KindUnknown and `zfsErr` (`internal/endpoint/endpoint.go:198`) returns **Internal**. The caller's input is permanently invalid on that host, but CSI retries Internal with backoff indefinitely.
- **Minimal fix:** gate `disabled` on the userland version. Add `"must be one of"` and `"must be power of 2"` to the KindInvalidProperty patterns, and add `"pool must be upgraded"` (or map it to FailedPrecondition).

## F4 (MEDIUM): deterministic pool or feature rejections of allowlisted values come back as Internal, not InvalidArgument or FailedPrecondition

- **Where:** `internal/endpoint/props.go:106-107`, which accepts zstd and recordsize up to 16M without regard to the pool. The pattern table is in `internal/zfs/errors.go` (~107-137).
- **recordsize > 128K without the `large_blocks` feature:** libzfs refuses it before the ioctl, using the pool's maxblocksize (`libzfs_dataset.c:1041-1062`; `spa_maxblocksize`, `spa_misc.c:3054-3060`). The error is `'recordsize' must be power of 2 from 512B to 128K`, which classifies as Internal.
- **recordsize above `zfs_max_recordsize`:** a lowered tunable gives the kernel ERANGE (`zfs_ioctl.c:5458-5460`), which classifies as InvalidArgument (OK).
- **zstd without `zstd_compress` (or lz4 without `lz4_compress`):** the kernel returns ENOTSUP (`zfs_ioctl.c`, `zfs_check_settable` compression case). `zfs_create` then prints `pool must be upgraded to set this property or value` (`libzfs_dataset.c`, ENOTSUP case in `zfs_create`), which classifies as Internal.
- **Not a convergence break:** the kernel create is atomic. `zfs_ioc_create` and `zfs_ioc_clone` call `dsl_destroy_head` when setting props fails, so no half-made dataset is left. The only problem is the wrong code, which causes endless retries.
- **Minimal fix:** add the error patterns above. Optionally pre-check `large_blocks`, `zstd_compress` and `lz4_compress` against the pool's feature state.

## F5 (MEDIUM): `copies=2|3` silently cuts usable capacity on filesystems by 2-3x

- **Where:** `internal/endpoint/props.go:116` allows copies on filesystems. The quota is set at `internal/endpoint/plan.go:240-242`, which sets `refquota` (and, with reserve, `refreservation`) to the requested capacity.
- **Proof (OpenZFS):** `man/man7/zfsprops.7:1025-1028` says the space used by multiple copies is "charged to the associated file and dataset, changing the used property and counting against quotas and reservations".
- **Effect:** a 10Gi filesystem volume with `copies=2` holds about 5Gi of data, yet reports capacity 10Gi. That breaks the capacity contract the denied list says it protects.
- **Zvols are not affected:** they use `refreservation=auto`, and `zvol_volsize_to_reservation` multiplies by copies (`libzfs_dataset.c:5514-5543`).
- **Minimal fix:** for filesystems, either deny `copies`, or scale `refquota` and `refreservation` by copies and divide by copies when reporting capacity.

## Checked and found sound (no finding)

- **Compression names:** `gzip-6` → `gzip`, `zstd-fast-1` → `zstd-fast` (`ZIO_ZSTD_LEVEL_FAST_DEFAULT == FAST_1`, `include/sys/zio_compress.h:87`). `zstd` and `zstd-3` are distinct stored values, and both print as given. `on` prints `on` on both versions. The compression table is identical in 2.2.9 and 2.4.0.
- **acltype aliases:** they match the first-name rule, and the table is identical in 2.2 and 2.4.
- **Other index tables:** dedup, sync, logbias and copies are identical in 2.2 and 2.4.
- **volblocksize:**
  - Its source is `default` or `-`, never `local` (`dsl_prop_nvlist_add_uint64`, `dsl_prop.c:1318-1337`), and the code correctly compares it against `BlockSz`.
  - A clone with `-o volblocksize` is refused as readonly (`libzfs_dataset.c:1006-1010`, with `zhp` non-NULL in `zfs_clone`, `libzfs_dataset.c:3978`), and `cloneBlockSize` handles that.
  - The 128K cap is stricter than libzfs, which allows up to the pool maxblocksize, so it only restricts and never breaks anything.
- **recordsize upper bound:** zfs-2.2.9 already defaults `zfs_max_recordsize` to 16M (`dsl_dataset.c:88`), so 16M is not a 2.2-vs-2.4 split. The only gate is the `large_blocks` feature (F4).
- **`-o k=v` injection:** keys come only from the allowlist, user properties (`:`) are refused, values are canonicalised into fixed sets or decimal integers, and commands run through `exec.CommandContext` with no shell (`internal/zfs/runner.go:39`).
- **Sim type gating:** it matches the `zfs_prop.c` type masks used by `zfs_valid_proplist`.
- **Committed volume changed later by an admin:** `zfs set` or `zfs inherit` leads to AlreadyExists on a later retry. That is consistent with the documented "set locally" rule (OpenZFS reports the source accurately) and is by design.

Dropped as unsubstantiated: mount-option "temporary" source for atime and relatime causing a mismatch on retry. libzfs does override the source (`get_numeric_property`, `libzfs_dataset.c:2017+`), but I could not show a real path where a retry happens while a contradicting mount exists in the endpoint's namespace.
