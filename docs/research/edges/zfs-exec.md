# ZFS execution edge

## EDGES

- Daemon to kernel/userland ZFS: either shell-out to `zfs`/`zpool` (go-zfs, zrepl, openebs) or libzfs/libzfs_core via cgo (go-libzfs).
- Shell-out contract: `zfs list -H -p -o ...` (github:zrepl/zrepl@d916c93c internal/zfs/zfs.go:169, 238), `zfs get -Hp -o name,property,value,source` (zrepl zfs.go:1625), `zfs get -Hp` (github:mistifyio/go-zfs@2a28eb65 zfs.go:315), `zpool get -H -p -o value` (zrepl internal/zfs/resume_token.go:131).
- Process boundary: go-zfs `command.Run` captures stdout/stderr into buffers and returns `&Error{Err, Debug, Stderr}` (go-zfs utils.go:51-90), with optional context timeout and SIGTERM (utils.go:53-63).
- Error envelope: openebs `pkg/zfs/errors.go:37-69` `Error{Op, Dataset, Stderr, Err}` with `Unwrap()`, stderr kept verbatim. Execution via `runCmd` = `CombinedOutput` (openebs pkg/zfs/zfs_util.go:74-81), so stderr and stdout are merged.
- cgo boundary (go-libzfs): `#cgo LDFLAGS: -lzfs -lzpool -lnvpair` (github:bicomsystems/go-libzfs@f9d12fe5 common.go:12-13). Errors come from `libzfs_errno`/`LastError()` (common.c:16, zfs.go:132-136).

## SEAMS

1. Command runner (exec, timeout, logging, stdio capture). Best existing: zrepl `internal/zfs/zfscmd/zfscmd.go:30-72`, which wraps `exec.CommandContext`, logs start/end, and exposes `CombinedOutput`/`Output` plus an error-buffer variant. Its comment at lines 34 and 44 notes that `ExitError.Stderr` is set only in some paths, so callers must choose deliberately.
2. Property read (typed `zfs get -Hp`). Best existing: zrepl `internal/zfs/zfs.go:1624-1716`, which requests `name,property,value,source`, checks tuple shape, and returns a per-dataset map.
3. Error classification. Best existing: zrepl `zfs.go:1546-1561`, a regex on stderr, `^cannot open '([^)]+)': (dataset does not exist|no such pool or dataset)`, marked "verified in platformtest", mapped to typed `DatasetDoesNotExist`. zrepl also uses it in ZFSListChan (zfs.go:286-290) and ZFSDestroy (zfs.go:1824-1827). Caveat: this is string matching on stderr.
4. Library choice: go-libzfs (cgo) gives errno-level results but depends on libzfs headers. go-fsctl/zfs (BSD-3, github:go-fsctl/zfs@f9af25b2, README.md:10-14) implements libzfs_core as pure Go over `/dev/zfs` ioctls, with no cgo and no shell-out (abi.go:46-62 lists `ZFS_IOC_*` values).

## GAPS

- No surveyed library gives typed, errno-based classification of "does not exist". zrepl is the only one with a mapped typed error, and it relies on regex over stderr. openebs returns raw stderr only (errors.go:33-36, "no pattern matching").
- go-zfs (mistifyio) and go-libzfs were not verified for error classification beyond the cited lines. Not verified: go-zfs maintainers' last commit date (indexed HEAD 2025-10-04 committed), go-libzfs (indexed HEAD 2022-04-19, LICENSE not indexed, so the license is unverified). openebs (Apache-2.0, HEAD 2026-10-08) and zrepl (MIT, HEAD 2026-03-17) licenses were confirmed.
- openebs does not parse `-Hp` output in the matches found (zero grep hits for `-Hp`); its parsing approach is unestablished.
- Q3: no production Go project using libzfs_core cgo was found. zrepl is CGO_ENABLED=0 with os/exec (research result). The only lzc cgo sample is a GitHits-distilled example, not a production source. go-fsctl/zfs avoids cgo entirely. Build and version-skew costs remain unestablished. Only indirect evidence: zrepl changelog notes an OpenZFS 2.0 FreeBSD timeout bug.
