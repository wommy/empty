p='ci/real-zfs-guest.sh'
s=open(p).read()
def rep(a,b):
    global s
    assert s.count(a)==1,a
    s=s.replace(a,b)
rep("""# which end exportfs's read of the file for every dataset; with them
# TestZFSDiffReplay, whose share sequences run `zfs set sharenfs` and so
# exportfs -ra on the same table), the csi
# tests other than""","""# which end exportfs's read of the file for every dataset; nightly, with
# ZFSDIFF_FULL=1, TestZFSDiffReplay joins them: its whole corpus turns
# shares on, which runs exportfs -ra on the same table), the csi
# tests other than""")
rep("""shard() {
  awk 'FNR == NR""","""nfs='^(TestFileExport|TestNFSReplay$)'
[ "${ZFSDIFF_FULL:-}" = 1 ] && nfs='^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)'
shard() {
  awk -v nfs="$nfs" 'FNR == NR""")
rep("""g = ($1 ~ /^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)/) ? "nfs" : $1""","""g = ($1 ~ nfs) ? "nfs" : $1""")
rep("""# TestZFSDiffReplay's report (internal/endpoint/zfsdiff_replay_test.go):
# real pool vs simulator vs reference model. ZFSDIFF_FULL=1 (nightly)
# replays the whole corpus instead of the PR sample
export ZFSDIFF_REPORT=$out/zfsdiff.log
export ZFSDIFF_FULL=${ZFSDIFF_FULL:-}
""","""# TestZFSDiffReplay's report (internal/endpoint/zfsdiff_replay_test.go):
# real pool vs simulator vs reference model. ZFSDIFF_FULL=1 (nightly)
# replays the whole corpus instead of the PR sample, which turns no share
# on (zfsdiff.PRSample) and so need not share the NFS tests' shard
export ZFSDIFF_REPORT=$out/zfsdiff.log
export ZFSDIFF_FULL=${ZFSDIFF_FULL:-}
""")
open(p,'w').write(s)
p='ci/real-zfs-weights.txt'
s=open(p).read()
rep("""# and fails past 15s. The whole corpus replays nightly (ZFSDIFF_FULL=1).
# Sharded with the NFS tests: its share sequences rewrite zfs.exports.
""","""# and fails past 15s. The whole corpus replays nightly (ZFSDIFF_FULL=1),
# sharded with the NFS tests then, since its share sequences rewrite
# zfs.exports; the PR sample turns no share on.
""")
rep("sequences (244 of 1,228","sequences (242 of 1,228")
open(p,'w').write(s)
