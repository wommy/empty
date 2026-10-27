# Lane ledger: qualify and quantify readers and writers separately

One JSON record per finished agent in ledger.jsonl; `python3 -I score.py` prints per-lane tables.
Cost is measured the same way in both lanes; quality and value are lane-specific.

## Common (cost)
tokens, tool_uses, minutes, model, wave

## Readers (scouts, advisors, adversaries, architects working only in scratch copies)
- claims: findings asserted
- checked: claims the coordinator or a later agent verified against HEAD
- confirmed: checked claims that held (real bug, real gap, real speedup)
- wrong: checked claims that were false or stale at HEAD
- bugs: confirmed real defects in shipped code (weight 5)
- anomalies: confirmed unexplained-behaviour finds root-caused (weight 3; owner rule)
- adopted: recommendations that became a merged change (weight 2)
precision = confirmed / checked; stale = wrong / checked
value = 5*bugs + 3*anomalies + 2*adopted + 1*(confirmed - bugs - anomalies - adopted)

## Writers (builders that edit and commit)
- commits merged, files, lines (+/-)
- bugs_fixed (weight 5), tests_added, rows_newly_killed (spec rows or mutants)
- seconds_saved (PR wall clock, measured on CI; 1 point per 10s)
- runner_seconds_saved (summed job time per PR, measured on CI; 1 point per 60s)
- defects: problems the writer introduced that were caught after hand-off
  (review, CI red, a later fix); each costs -4
- conflicts: merge conflicts needing manual resolution (-1 each)
- red_ci: CI runs turned red by the writer's change (-3 each)
value = 5*bugs_fixed + rows_newly_killed/5 + seconds_saved/10 + runner_seconds_saved/60 + tests_added/5 - 4*defects - conflicts - 3*red_ci

## Score
value per 100k tokens, and value per hour, per agent and per lane.
The coordinator (me) is recorded as its own writer row: its merges and red CI count too.
