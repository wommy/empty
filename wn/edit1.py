p='ci/real-zfs-weights.sh'
s=open(p).read()
def rep(a,b):
    global s
    assert s.count(a)==1, a
    s=s.replace(a,b)
rep('''# a job log as the TSV the guest wrote:''','''# the guest's load report (a job log, or a TSV's sibling load.txt) as a
# header line: its fork count and root cgroup CPU-seconds over the phase
loadhdr() {
  awk '
    { sub(/^[^0-9]*[0-9][0-9-]*T[0-9:.]+Z /, "") }
    $1 == "forks" && $2 ~ /^[0-9]+$/ { fk = $2 }
    $1 == "root" && $2 ~ /^[0-9]+(\\.[0-9]+)?$/ && $3 == "(sys" { cpu = $2 }
    END { if (fk != "" || cpu != "") print "# load: forks=" (fk == "" ? "-" : fk) " cpu_s=" (cpu == "" ? "-" : cpu) }
  ' "$1"
}

# a job log as the TSV the guest wrote:''')
rep('''  if [ -s "$d/tsv" ]; then
    cat "$d/tsv"''','''  loadhdr "$f"
  if [ -s "$d/tsv" ]; then
    cat "$d/tsv"''')
rep('''    f=$logs/$i.tsv
  fi
  files+=("$f")''','''    f=$logs/$i.tsv
  elif [ -f "$(dirname "$f")/load.txt" ]; then
    # an artifact's TSV: its part's load report sits beside it
    i=$((i + 1))
    { loadhdr "$(dirname "$f")/load.txt"; cat "$f"; } >"$logs/$i.tsv"
    f=$logs/$i.tsv
  fi
  files+=("$f")''')
rep('''    if ($0 ~ /mode=full/) full = 1''','''    if ($0 ~ /mode=full/) full = 1
    # the part'"'"'s guest work, to normalize its rows by (loadhdr)
    if (match($0, /forks=[0-9]+/)) forks[FILENAME] = substr($0, RSTART + 6, RLENGTH - 6) + 0
    if (match($0, /cpu_s=[0-9.]+/)) cpus[FILENAME] = substr($0, RSTART + 6, RLENGTH - 6) + 0''')
rep('''    t = f[2]; par = f[4] + 0; own = f[5] + 0; cost = f[6] + 0; wall = f[7]
    if (par) w = cost / nproc
    else if (wall != "-") w = wall + 0
    else w = own + (cost - own) / nproc
    # a split test (ci/real-zfs-plan.sh) has a row per shard of the run,
    # over the files of its parts: they add up within a run, runs average
    sum[t] += w
    if (!((key, t) in run)) { run[key, t] = 1; cnt[t]++ }''','''    t = f[2]; par = f[4] + 0; own = f[5] + 0; cost = f[6] + 0
    # parallel test, or parallel subtests: they share the nproc vCPUs
    w = (par || cost > own) ? cost / nproc : own
    # a split test (ci/real-zfs-plan.sh) has a row per shard of the run,
    # over the files of its parts: they add up within a run, runs average
    val[FILENAME, t] += w
    tot[FILENAME] += w
    if (!((key, t) in run)) { run[key, t] = 1; cnt[t]++ }''')
rep('''  END {
    print "# Hand-set''','''  END {
    # each part in its own guest work: forks if every part has them, else
    # root CPU-seconds, else raw seconds
    nf = nfk = ncpu = 0
    for (f in tot) { nf++; if (f in forks) nfk++; if (f in cpus) ncpu++ }
    by = !nf ? "" : nfk == nf ? "forks" : ncpu == nf ? "CPU-s" : ""
    if (nf && by == "") print "real-zfs-weights: not every part has a fork count or CPU-seconds (a job log, or load.txt beside a TSV): weights are raw seconds, not normalized per part" >"/dev/stderr"
    for (f in tot) {
      b = by == "forks" ? forks[f] : by == "CPU-s" ? cpus[f] : 0
      k[f] = (by != "" && tot[f] > 0) ? b / tot[f] : 1
      if (by != "") printf "real-zfs-weights: %s: %.2f s measured, %s %s: %.1f per s\\n", f, tot[f], b, by, k[f] >"/dev/stderr"
    }
    for (ft in val) {
      split(ft, x, SUBSEP)
      sum[x[2]] += val[ft]; norm[x[2]] += val[ft] * k[x[1]]
    }
    # back to seconds: the total the raw seconds average to
    R = U = 0
    for (t in sum) { R += sum[t] / cnt[t]; U += norm[t] / cnt[t] }
    for (t in sum) wt[t] = U > 0 ? norm[t] / cnt[t] * R / U : sum[t] / cnt[t]
    print "# Hand-set''')
rep('''    print "# (sequential test: wall, parallel subtests included; parallel test:"
    print "# test-seconds / nproc). Edit by hand only with a `# note:` line; rerun the script on newer"
    print "# runs when shards drift apart (shards.txt in the job summary)."''','''    print "# (a parallel test, or one with parallel subtests: test-seconds / nproc;"
    print "# any other: its own time), each part'"'"'s rows scaled to the part'"'"'s guest"
    print "# work (forks, else CPU-s: a loaded part reads 2-4x high) before runs"
    print "# average, then rescaled to the measured seconds'"'"' total. Edit by hand"
    print "# only with a `# note:` line; rerun the script on newer runs when shards"
    print "# drift apart (shards.txt in the job summary)."''')
rep('''    for (t in sum) printf "%s %.2f\\n", t, sum[t] / cnt[t] | "sort''','''    for (t in wt) printf "%s %.2f\\n", t, wt[t] | "sort''')
open(p,'w').write(s)
