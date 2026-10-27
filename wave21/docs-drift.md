# Docs drift audit (HEAD 4a63496, read-only)

Scope: README.md, CONTEXT.md, docs/adr/*.md, deploy/ unit comments, flag help (cmd/*), and CI comments where they restate the ADRs.
Ranked by how likely a reader or operator is misled. Line numbers are at HEAD 4a63496.

Checked and found consistent (no action): every flag name and default in README/ADRs against cmd/zfs-endpoint/main.go:87-112, cmd/zfs-endpoint/reconcile.go:46-60 and cmd/zfs-csi/main.go:235-241; the -lock-wait / -nfs-min-prefix (16) / -min-free-bytes (0) / -property-prefix (zfs-endpoint) / 126-byte parent defaults; the socket layout, modes and 70/69-byte path budget (unit comment, ADR 0001); D3's digest-before-max_deletions order (internal/endpoint/reconcile.go:681-695); the guid check before destroy (apply.go:84-121, ADR 0007); the locked re-plan keyed by guid (README:140); the reconcile timer and service comments; ADR 0001 D7 (unit and manifests set no -socket-group; controller.yaml:57 runs as uid 0); every test name cited in README and ADR 0001/0005/0007 exists. No zed-is-stopped or udisks2-masked text remains in README, CONTEXT, ADRs or deploy/; only the stale section reference at #5.

---

## 1. ADR 0005 "ssh poll and module load": the cause, the mechanism and the predictions are all superseded  (high)

The section still gives the slirp/banner theory and the 0.3s probe. CI showed the delay was OpenSSH PerSourcePenalties. Logins refused by pam_nologin while /run/nologin stood were charged authfail (5s each), and each abandoned banner probe was charged noauth (1s). Past the 15s minimum, sshd refused the slirp source (10.0.2.2) outright. The fix was 6629b7e, which gates logins on the serial getty's login prompt, and ssh now comes up on the first probe.

| ADR 0005 line | Says | Code / evidence | Rewrite |
|---|---|---|---|
| 203 | "The cause is the poll. slirp's hostfwd accepts the TCP connect at once ... SYN ... lost ... each try could stall for 5s" | Disproved: runs 40519558807 (0.3s probes: sshd seen 18-19s late, 179-191 probes) and 40535710051 (1s probes: still 16-19s, 8 penalised drops per boot); wave20/sshpenalty.md §3, §7 | "The lag was not the poll. sshd refused logins while /run/nologin existed (pam_nologin, `account required` in Ubuntu's sshd PAM stack, until systemd-user-sessions). OpenSSH ≥ 9.8 charged each refused login as authfail (5s) and each probe that left before authenticating as noauth (1s). Past 15s, it refused every connection from the source with 'Not allowed at this time' until the charge expired. All host traffic reaches the guest from one address (slirp 10.0.2.2)." |
| 208 | "The poll reads the banner, bounded at 0.3s ... Every 40th probe (about 16s) waits 5s" | ci/real-zfs-vm.sh:300-317: `banner 1` on every probe (1s bound), no 40th-probe 5s wait, 0.1s sleep. Logins are first gated on a ` login:` line in console.log (302-312), with a 60s fallback to plain polling (`gate`), and the gate is re-armed after a GRUB reboot (324) | "**Logins wait for the serial getty's login prompt** (`login prompt` mark; getty starts after systemd-user-sessions, which removes /run/nologin). Then each probe reads the banner with a 1s bound and requires `SSH-`, because a penalised source reads 'Not '. Only then does ssh log in. With no prompt in 60s, it polls as before. The deadline is 600s. The 0.3s probes made things worse: each one was a noauth charge (d0aae91)." |
| 221-223 | Predictions −1.35..−2.35s endpoint, −5..−8.5s csi; "push-to-green is still bound by the endpoint job" | Superseded by the penalty finding; the coordinator reports ssh up on the first probe | Replace with the measured result of the getty gate: the run id, `ssh up (1 probes)` and the time from `login prompt` to `ssh up`. Keep the masks' prediction separate, since it is unaffected. |
| 225 | must confirm "`ssh up (N probes)` within about 0.4s of ssh.service" | Not what the code now measures | "`login prompt` then `ssh up (1 probes)`; the `sshd penalties:` line (vm.sh:469) prints the setting; no `drop connection ... penalty` lines in the ssh journal block (vm.sh:470)" |

The same stale text is in **ci/real-zfs-vm.sh:282-290** (comment): "read with a 0.3s bound ... host sees sshd within about 0.4s". The code at :317 is `banner 1`. Correction: "the guest's sshd banner, read with a 1s bound once the console shows the getty's login prompt (below); slirp's lost SYN made 5s ssh attempts stall, and sub-second probes were each charged noauth by PerSourcePenalties". Drop the 0.4s claim.

## 2. README Testing paragraph: CI shape is two jobs, it is now three  (high for readers of CI)

- **README.md:110**: "one job and VM per suite (`ci/real-zfs-suites.txt`: endpoint, csi), each running three concurrent shards".
- Code: .github/workflows/ci.yml:189-191 has three VM jobs, `{endpoint 0/2}`, `{endpoint 1/2}` and `{csi}`. Each endpoint part runs 3 of one 6-shard plan (ci/real-zfs-plan.sh, check-split).
- Correction: "CI does the latter in three Ubuntu VMs running OpenZFS 2.4 (`ci/real-zfs-vm.sh`): the endpoint suite in two parts, each running 3 shards of one fixed 6-shard plan (`ci/real-zfs-plan.sh`, checked by `ci/check-split.sh`), and the csi suite in one. Each shard is on its own pool backed by a sparse file on a guest tmpfs. Nightly runs one VM per suite, unsplit."

## 3. ADR 0005 body still says "one job and VM per suite" / "two VM jobs"  (medium)

- **:39** "the real-ZFS VM suites, one job and VM per suite (see the two VM jobs below)". Should read: "one VM job per part of a suite: endpoint in two parts, csi in one (see 'The endpoint suite in two parts')".
- **:56** "the endpoint job three endpoint shards". Should read: "each endpoint part job 3 of the 6 endpoint shards".
- **:78** "Two real-ZFS VM jobs, one per suite (adopted, wave 15)". Append: "; the endpoint suite split in two parts since wave 18 (three VM jobs)".
- **:42** "`real-zfs-full`: the same suite matrix as `ci.yml` (`endpoint`, `csi`), one VM per suite". nightly.yml:128 is `suite: [endpoint, csi]` with no parts, so endpoint runs unsplit (ZFSVM_PART defaults to 0/1, vm.sh:110), and the csi leg boots through BIOS/GRUB (`ZFSVM_INITRD=1`, nightly.yml:182). Should read: "the same suites as `ci.yml`, one VM each, endpoint unsplit (one part); the csi leg boots with `ZFSVM_INITRD=1`, the only run of the GRUB fallback path".
- Same stale wording in **.github/workflows/ci.yml:28** ("the real-ZFS VM jobs (one per suite)") and in the **ci/real-zfs-suites.txt** header ("ci.yml and nightly.yml run one job per suite"; "check-split.sh that each endpoint test ran in exactly one shard"). Should read: "one job per part of a suite (ci.yml splits endpoint in two)"; "...exactly once over all six shards of both parts".

## 4. ADR 0005: contradictory statements of what binds push-to-green, and predictions that later runs overtook  (medium)

- **:152** "Push-to-green is then bound by csi (about 54s today...)" and **:161** "the csi job is now the last to finish". These contradict **:223** "push-to-green is still bound by the endpoint job". :223 was written for the ssh section, and that section's predictions are superseded anyway (#1). Pick one, from a measured run.
- **:160** predicted each part's `guest load` phase "near 12s". Measured: 10.4s, 6.6s and 8.5s (runs 40536056973, 40536341101, 40536492611; already cited at :195-196). Add one line under the split's confirmations: "Measured: 6.6-10.4s per part."
- **:229-235** mitigations=off init_on_alloc=0 says "Adopt it if the endpoint phase drops by at least 1s. Otherwise revert it." No outcome is recorded, yet vm.sh:165 still applies it. Record the call, or mark it "kept, unmeasured: no A/B run".
- **:3** Status "accepted (budget, 45s nominal margin)" is still true. :97/:121/:152 track the margin as unmet, which is fine.

## 5. ci/real-zfs-guest.sh:103 points at a section the revert deleted  (medium-low)

- Says `(ADR 0005, "quiet guest")`. 4a63496 removed that section. The counters now live under "Where the endpoint phase's CPU goes" (ADR 0005:189-196).
- Correction: `(ADR 0005, "Where the endpoint phase's CPU goes")`.
- Also ADR 0005:196 says "under this section's keep rule". The rule's own bullet went with the old section; the parenthesis restates it, so this is acceptable. "the keep rule recorded in 9a8b891's ADR text" would be clearer.

## 6. ADR 0005 two-part exactly-once argument omits the row-total pin it now depends on  (medium-low)

- **:146-148** argue both jobs make the same plan and "Rows `i mod 6` over i = 0..5 cover each row once". That needs both parts to split the same TestPropertyRoundTrip total T, which neither job can see of the other.
- 3d6b248 made that a check: `propRTRows = 196` (internal/endpoint/props_roundtrip_test.go:39), and check-split.sh:86-117 checks every shard's logged total against it. It also added empty logs for planned-empty shards (check-split.sh:25, :94).
- Add to :147: "and each shard's T must equal the committed `propRTRows`, which the test itself pins to `len(allowlistRows())`, so both parts split the same total; a shard the plan gives no test writes an empty log, which passes only as that."

## 7. README errors list misses the guid-mismatch ABORTED  (medium-low, operator-facing)

- **README.md:42** lists ABORTED only for a busy lock and for an unclassified failure after an earlier op.
- Code: apply.go:112-120 returns Aborted ("... is not the dataset the plan inspected ... replaced since; retry") before any destroy whose target's guid changed since the scan (OWN-15, ADR 0007:108). This hits DeleteVolume, DeleteSnapshot and Reconcile.
- Append to :42: "It is also returned when a destroy's target no longer has the guid the plan read (renamed away and replaced since the scan); the retry re-plans against what is there."
- The decisions table (:74) could also note "and each destroy checks the target's guid first".

## 8. README Runbook: max-deletions and partial-apply exit status  (low-medium)

- **README.md:127**: "More than `-max-deletions` (default 10) undesired volumes refuses the whole run." The cap is checked only on apply (internal/endpoint/reconcile.go:678 returns a dry run before :690), so a dry run over the cap succeeds and lists every action, and only `-apply` fails with FAILED_PRECONDITION.
  - Correction: "...refuses the apply (FAILED_PRECONDITION, nothing applied); the dry run still prints the whole plan, so count its `not in the desired set` actions first."
- **README.md:128**: "a group that failed is listed in `skipped` as `failed: <error>`". Since 305759f, `-apply` exits 1 after printing when any group failed or was not attempted (cmd/zfs-endpoint/reconcile.go:127-135).
  - Add: "...and the command exits 1, so a wrapper never reads a partial apply as done."

## 9. README headroom: the clone sizing is not described  (low)

- **README.md:45** lists what a plan reserves as "a thick zvol's volsize, a filesystem's `reserve`, a grow's delta".
- Since 305759f, a thick clone/restore is sized from its source snapshot's volsize, not the volume's current one (internal/endpoint/headroom.go OpClone branch).
- `reserve` is the request/StorageClass flag (internal/csi/controller.go:38), which becomes `refreservation`.
- Suggested: "(a thick zvol's volsize, a thick clone's snapshot volsize, a `reserve` filesystem's refreservation, a grow's delta)".

## 10. ADR 0003 Consequences reads as current when it is done  (low)

- **docs/adr/0003...:31** "the default `-property-prefix`, today `democratic-csi`". :35 says all three are done.
- Change to "then `democratic-csi`".

## 11. ADR 0002 (proposed) cites stale code locations  (low)

- `volumeID` is at internal/endpoint/endpoint.go:403, not `endpoint.go:145`.
- `snapComponent` is at plan.go:1007, not `plan.go:380`.
- The other `file:line` cites (plan.go:347-351, endpoint.go:497, 264-280, table.go:122-160, zfs.go:43) predate the code moving too.
- The proposal itself is not built (no `zs1-` in code), which matches its status. Cite functions instead of lines.

## 12. ADR 0006: "0700-directory check" is approximate  (low)

- **docs/adr/0006...:61** "the endpoint socket keeps the strict 0700-directory check".
- SecureDir (internal/unixsock/unixsock.go:256-271) refuses a directory writable by group or others. A 0710 `-socket-group` directory passes (ADR 0001:132).
- Should read: "keeps the strict check (a directory owned by root or the daemon and writable by no one else)".

## Terms vs CONTEXT.md

No conflicting use found in README, ADRs or deploy comments. "Reconcile/GC" (README:136) is a heading, not the avoided "GC loop". CONTEXT's **Snapshot** entry (`<volume id>@<name>`) omits that the component can be `csi-<32 hex>` when the name is not a valid zfs component or too long (`snapComponent`). That is optional: "`<volume id>@<component>`, the CSI name or its hash".
