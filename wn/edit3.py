p='docs/adr/0005-one-minute-ci-budget.md'
s=open(p).read()
old="""  - The weights still come from `test-times.tsv`. There is one per part: in the test step's log on a green run, and in the artifact `real-zfs-report-endpoint-<p>` on a failed one. `ci/real-zfs-weights.sh` reads either, and sums the parts of a run (the `run=` header) before it averages runs.
"""
new="""  - The weights still come from `test-times.tsv`. There is one per part: in the test step's log on a green run, and in the artifact `real-zfs-report-endpoint-<p>` on a failed one. `ci/real-zfs-weights.sh` reads either, and sums the parts of a run (the `run=` header) before it averages runs.
  - **Each part is normalized first (wave 30).** A test's seconds depend on the part it was measured in. In the loaded part the same test reads 2-4x higher than in the light one (TestZFSDiffReplay: 3.8-6.4s in part 0, 1.7s in part 1). Fed back, that moved tests between parts at inflated weights, and the plan swung from one part to the other.
    - So the script scales each part's rows (one run, one part) to sum to that part's guest work before it averages runs. The guest work is its fork count, the `forks N` line of the guest's load report (in the job log, or `load.txt` beside an artifact's TSV). Forks are zfs(8), mount and exportfs execs, so they count work, not contention. Without a fork count the root cgroup's CPU-seconds stand in. With neither for every part the rows stay raw seconds, and stderr says so.
    - Then the averages are rescaled to the total that the raw seconds average to, so the file stays in seconds while its balance is in guest work.
    - A test that is parallel, or has parallel subtests, weighs its test-seconds / nproc in both input formats. Any other test weighs its own time. Wall time is not used: a TSV gave TestCrashAtEveryMutationConverges its wall (8.5s) and an old log gave it cost / nproc (15.2s).
    - `ci/weightstest` checks this on a two-part fixture whose weights are known.
"""
assert s.count(old)==1
s=s.replace(old,new)
anchor="- **Host build at nice 19 (wave 27, on probation).**"
assert s.count(anchor)==1
add="""- **Normalized weights, wave 30.** The weights were regenerated from runs 40550837228, 40551637783, 40551837712 and 40552465632, both parts of each, with each part normalized to its forks. Part 0 had made 1.03-1.37x the forks of part 1 in every run, 55/45 of the work even after the wave 27 rebalance. The fork-unit model of those runs predicts the plan at 620cda8 was 44.8/55.2 and is now 50.1/49.9 (50.0-50.4 for the models of r632 alone and of the three older runs). Expected: part 0 drops from about 55% of the forks to about 50%, and the parts' phases then differ by host speed alone. To confirm on the first green runs: the two parts' `forks` lines within about 5% of each other.
"""
s=s.replace(anchor,add+anchor)
open(p,'w').write(s)
