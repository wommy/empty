# Adversarial review: 0ea9575..6f3fa82 (ci-poll + ci-split3)

Verdict: nothing blocks. The findings below are latent or low.
Run 40519558807 (2f7cf43, green) confirms these paths: modprobe, ordering, check-split, plan, artifacts.

## wait_ssh / PerSourcePenalties (evidence)
- What the run shows: 191 probes in 28.7s is about 0.15s per probe, and each probe includes a 0.1s sleep. So most probes failed fast, on EOF or a refused connection, rather than waiting out the 0.3s read. Guest userspace was done at 9.0s, which leaves about 130 fast failures *after* sshd was up.
  - A healthy sshd sends its banner within milliseconds.
  - A slirp SYN that is lost gives a 0.3s timeout, not a fast failure.
  - Fast closes while sshd is up are what a penalised source sees: sshd drops the connection right after accept, with no banner.
  - This supports the PerSourcePenalties hypothesis. The defaults are noauth:1s per pre-auth disconnect, and blocking starts once the penalty passes min:15s. Every probe comes from 10.0.2.2 (slirp).
  - The old poll (`vm true`) authenticated on its first contact, so it never built up penalties.
- Local test (bash 5): `banner` returns rc 1 at once on a refused connection, rc 142 after the 0.3s bound on a silent listener, and 0 on `SSH-`. Under `set -euo pipefail` a failure inside the `&&` chain never trips -e.
- How to confirm: run `journalctl -u ssh | grep -i penalt` in the guest. It should show "drop connection ... penalty: noauth".
- Possible fixes:
  - stop probing pre-auth;
  - or `PerSourcePenaltyExemptList 10.0.2.2/32` (or `PerSourcePenalties no`) via the cloud-config. That changes the cache key, so the next run is a cold one.

## Checked, OK
- **Merge resolution (ci/real-zfs-vm.sh:395-415):**
  - `wait "$modprobe"` runs before `ver=$(vm 'zfs version')`, and the exit status propagates (`exit $r`).
  - `$ver` is set once (398), before its uses at 404 and 412-415. The version check runs once.
  - ci-split3's part suffix is kept in the summary heading (401).
  - Nothing between 374 and 395 needs zfs: cloud-init, build.info, uname, findmnt/blkid, exportfs.
  - Tests start only after line 454. The async ssh gets stdin=/dev/null.
- **GRUB fallback (no masks):** zfs is already loaded, so modprobe is a no-op. zed was already started, so `start --no-block` is a no-op. mitigations=off is absent, which only affects the summary line.
- **Nightly (one part):**
  - ZFSVM_PART defaults to 0/1, giving 3 shards.
  - ZFSDIFF_FULL=1 is in the host env, so the plan's NFS group includes TestZFSDiffReplay. check-split recomputes the plan with the same env.
  - The artifact is still `zfsdiff-report-full-*` and was never renamed. No job references bare `zfsdiff-report` any more (grep).
- **cache-warm:** suite=all with PROVISION_ONLY. It exits at 429, before any plan or part logic. Its `ns:` list is unchanged and check-suites passes locally.
- **check-skips with 0 allowed in a part** (tested):
  - With SKIPS_ELSEWHERE set to the whole list, it prints "0 allowed (and 22 elsewhere)", rc 0.
  - A skip in that part is flagged as unexpected.
  - An empty other-list works, because the script uses `FILENAME == ARGV[1]` and not NR==FNR.
- **check-split and the part-list awk:** run under mawk with fake logs for both parts. Both pass, and the rows add up (101+99=200). The split test goes only into each part's own list.
- **real-zfs-weights.sh:** the `run=` key is parsed from the second header line. Old TSVs without it fall back to a per-file key. The usage text uses the new artifact names. It has no consumers in the workflows.
- **ci.yml:**
  - The matrix expressions are fine.
  - The Go cache save is gated on `part == 0`, which also covers csi.
  - The guest save is csi-only.
  - There are no `needs:` on real-zfs.

## Findings, ranked
1. **Required-check names changed** (ci.yml:178).
   - Problem: the jobs are now "real ZFS 2.4 (endpoint 0/2, Ubuntu 26.10 VM)" and "(endpoint 1/2, ...)". If a ruleset or protection requires the old "(endpoint, Ubuntu 26.10 VM)", PRs wait forever for it.
   - What I could check: `rules/branches/main` returns []. The default branch is claude/gallant-johnson-1qaxp5, and classic protection returned 403, so I could not verify it.
   - Fix: update the required checks.
2. **Exactly-once across parts relies on determinism and is not compared across jobs** (check-split.sh:72, 97-100).
   - Problem: each part checks the row total `tot` only among its own shards, and the list/plan sha256 is only printed. If `allowlistRows()` or the list differed between the two jobs (e.g. different image), rows could be missed or doubled without an error. Both jobs use the same commit and guest, so this is unlikely.
   - Fix (optional): pin the expected total, or upload `tot` and the sha256s and compare them in a later job. That would cost the "no job waits" property.
3. **Latent: an empty shard fails the job** (real-zfs-guest.sh:142, real-zfs-vm.sh:500).
   - Problem: if a shard gets no tests, the guest `continue`s and writes no log. `cat "${eplogs[@]}"` then sets rc=1, and check-split reports "2 shard logs". Today TestPropertyRoundTrip is in every shard, so this cannot happen. If it is renamed or removed and #tests < 6, it can.
   - Fix: write an empty log for an empty shard, or skip missing logs in both places.
4. **Low: an early exit at 380-393 orphans the background modprobe ssh** (vm.sh:372-374). cleanup's `ssh -O exit` ends it. Harmless.
5. **Low: masked zfs-mount and zfs-share.** zfs-share (`zfs share -a`) can load zfs itself through libzfs while our modprobe runs. That race is harmless. If zfs-share fails it shows up only under "failed units" in the diagnostics, which is not a gate.
