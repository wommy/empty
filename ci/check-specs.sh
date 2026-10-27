#!/usr/bin/env bash
# Compare the csi-sanity specs one real-ZFS shard passed with the pinned
# list, exactly: a spec that stops passing (fails, starts skipping, is
# gone) or a new one fails the leg, by name, not only a count.
#
# usage: ci/check-specs.sh <leg> <shard> <passed> <pinned list>
#   ci/check-specs.sh vm csi-block "$o/csi-block.passed" internal/csi/testdata/sanity-passed-block.txt
#
# <passed>: TestSanity's CSI_SANITY_PASSED_OUT, one spec per line (written
# whether or not the run passed). <pinned list>: the same list TestSanity
# compares on the simulator (internal/csi/sanity_test.go, embedded in the
# binary), '#' lines comments. Missing either fails.
set -euo pipefail

leg=${1:?leg, e.g. vm}
shard=${2:?shard, e.g. csi-mount}
got=${3:?passed specs file}
list=${4:?pinned list}

[ -s "$list" ] || { echo "::error::$leg/$shard: no pinned list $list"; exit 1; }
[ -f "$got" ] || { echo "::error::$leg/$shard: no $got: TestSanity did not get as far as its report"; exit 1; }
want=$(sed -e '/^#/d' -e '/^[[:space:]]*$/d' "$list" | LC_ALL=C sort)
have=$(sed '/^$/d' "$got" | LC_ALL=C sort)
[ -n "$want" ] || { echo "::error::$leg/$shard: $list pins no spec"; exit 1; }
n=$(wc -l <<<"$want")
if [ "$want" = "$have" ]; then
  echo "csi-sanity $leg/$shard: the $n passed specs match $list exactly"
  echo "| $leg | $shard | csi-sanity: $n passed, exactly the pinned list | |" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"
  exit 0
fi
echo "::error::$leg/$shard: the csi-sanity specs that passed ($([ -z "$have" ] && echo 0 || wc -l <<<"$have")) differ from $list ($n)"
echo "(- pinned, did not pass: failed, skipped or gone; + passed, not pinned)"
# diff exits 1 on a difference; sed reads all of it
diff <(echo "$want") <(echo "$have") | sed -n -e 's/^< /- /p' -e 's/^> /+ /p' || true
echo "If intended, rewrite the list on the simulator (the command is in its header) and review the diff;"
echo "a difference only the real pool shows is a simulator gap to explain, not a list to widen."
exit 1
