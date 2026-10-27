p = '/home/user/empty/.claude/worktrees/agent-acf7494638986ab8a/ci/check-suites.sh'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a[:70]
    s = s.replace(a, b)


rep("""# - ci.yml and nightly.yml each have exactly one `suite: [...]` matrix, and
#   it lists every suite exactly once;""", """# - ci.yml and nightly.yml each have exactly one real-zfs matrix: a
#   `suite: [...]` line listing every suite exactly once, or `include:`
#   entries `- { suite: <s>, part: <p>, parts: <P> }` that give every
#   suite parts 0..P-1 exactly once each, one P per suite, and P > 1 only
#   for endpoint (the only suite with a shard plan, ci/real-zfs-plan.sh);""")
rep("""# (every endpoint test in exactly one shard, every TestPropertyRoundTrip
# row in exactly one shard).""", """# (the endpoint plan a partition of the list over every part, each part's
# logs exactly its shards' tests, every TestPropertyRoundTrip row in
# exactly one shard of all parts).""")
rep("""for wf in .github/workflows/ci.yml .github/workflows/nightly.yml; do
  m=$(matrix "$wf" suite)
  n=$(awk '{ print $1 }' <<<"$m" | sort -u | sed '/^$/d' | wc -l)
  [ "$n" = 1 ] || { err "$wf: $n suite matrices, want exactly 1"; continue; }""", """# parts <workflow>: "<suite> <part> <parts>" per `- { suite: s, part: p,
# parts: P }` include entry
parts() {
  awk '/^[[:space:]]*- *\\{ *suite:/ {
    s = $0; sub(/^[^{]*\\{/, "", s); sub(/\\}.*$/, "", s); gsub(/[ "\\047]/, "", s)
    c = split(s, f, ","); delete kv
    for (i = 1; i <= c; i++) { split(f[i], e, ":"); kv[e[1]] = e[2] }
    print kv["suite"], ("part" in kv) ? kv["part"] : "?", ("parts" in kv) ? kv["parts"] : "?" }' "$1"
}
for wf in .github/workflows/ci.yml .github/workflows/nightly.yml; do
  m=$(matrix "$wf" suite)
  p=$(parts "$wf")
  n=$(awk '{ print $1 }' <<<"$m" | sort -u | sed '/^$/d' | wc -l)
  if [ -n "$p" ]; then
    [ "$n" = 0 ] || { err "$wf: a suite matrix and include entries, want one of them"; continue; }
    got=$(awk '{ print $1 }' <<<"$p" | lines | sort -u)
    [ "$got" = "$suites" ] || {
      err "$wf: the include entries do not name every suite (< $file, > $wf):"
      diff <(echo "$suites") <(echo "$got") || true
    }
    bad=$(awk '$2 !~ /^[0-9]+$/ || $3 !~ /^[1-9][0-9]*$/ || $2 + 0 >= $3 + 0 { print "bad entry: " $0; next }
      { if (($1 in P) && P[$1] != $3) print $1 ": parts " P[$1] " and " $3; P[$1] = $3
        if (($1, $2) in seen) print $1 ": part " $2 " twice"; seen[$1, $2] = 1; c[$1]++
        if ($3 > 1 && $1 != "endpoint") print $1 ": split into " $3 " parts, only endpoint has a shard plan" }
      END { for (s in P) if (c[s] != P[s]) print s ": " c[s] " of " P[s] " parts" }' <<<"$p")
    [ -z "$bad" ] || err "$wf: the suite parts are not each part of each suite exactly once: $(tr '\\n' ';' <<<"$bad")"
    continue
  fi
  [ "$n" = 1 ] || { err "$wf: $n suite matrices, want exactly 1"; continue; }""")
rep("""ci.yml, nightly.yml and cache-warm.yml run each suite once\"""", """ci.yml, nightly.yml and cache-warm.yml run each suite (each part of it) once\"""")
open(p, 'w').write(s)
