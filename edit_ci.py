def edit(p, pairs):
    s = open(p).read()
    for a, b in pairs:
        assert s.count(a) == 1, (p, a)
        s = s.replace(a, b)
    open(p, 'w').write(s)

edit('ci/real-zfs-guest.sh', [
("""# (the NFS tests, TestFileExport* and TestNFSReplay, kept together: one
# nfsd, one export table; the replay writes malformed zfs.exports lines,
# which end exportfs's read of the file for every dataset), the csi""",
"""# (the NFS tests, TestFileExport* and TestNFSReplay, kept together: one
# nfsd, one export table; the replay writes malformed zfs.exports lines,
# which end exportfs's read of the file for every dataset; with them
# TestZFSDiffReplay, whose share sequences run `zfs set sharenfs` and so
# exportfs -ra on the same table), the csi"""),
("""g = ($1 ~ /^(TestFileExport|TestNFSReplay$)/) ? "nfs" : $1""",
 """g = ($1 ~ /^(TestFileExport|TestNFSReplay$|TestZFSDiffReplay$)/) ? "nfs" : $1"""),
("""export NFS_REPLAY_REPORT=$out/nfs-replay.log
""",
"""export NFS_REPLAY_REPORT=$out/nfs-replay.log
# TestZFSDiffReplay's report (internal/endpoint/zfsdiff_replay_test.go):
# real pool vs simulator vs reference model. ZFSDIFF_FULL=1 (nightly)
# replays the whole corpus instead of the PR sample
export ZFSDIFF_REPORT=$out/zfsdiff.log
export ZFSDIFF_FULL=${ZFSDIFF_FULL:-}
"""),
])

edit('ci/real-zfs-vm.sh', [
("""# six concurrent shards on six tmpfs-backed pools (ci/real-zfs-guest.sh);
# the logs come back per shard and are merged per check below
vm 'sudo bash real-zfs-guest.sh' || rc=$?""",
"""# six concurrent shards on six tmpfs-backed pools (ci/real-zfs-guest.sh);
# the logs come back per shard and are merged per check below.
# ZFSDIFF_FULL=1 (nightly.yml) replays the whole zfsdiff corpus
vm "sudo env ZFSDIFF_FULL=${ZFSDIFF_FULL:-} bash real-zfs-guest.sh" || rc=$?"""),
("""  echo "::error::vm/endpoint: TestNFSReplay wrote no nfs-replay.log"; rc=1
fi
""",
"""  echo "::error::vm/endpoint: TestNFSReplay wrote no nfs-replay.log"; rc=1
fi
# TestZFSDiffReplay replays zfs(8) sequences (internal/zfs/zfsdiff) on the
# pool, the simulator and the reference model. Divergences are reported,
# not failed (ZFSDIFF_STRICT=1 would); the summary gets the verdict per
# sequence, the workflow uploads the whole report. Its last line is the
# tally with the replay's wall time.
if [ -f "$o/zfsdiff.log" ]; then
  { echo "<details><summary>zfsdiff replay: $(tail -n 1 "$o/zfsdiff.log")</summary>"
    echo; echo '```'; sed -n '3,/^$/p' "$o/zfsdiff.log"; echo '```'; echo "</details>"; echo; } >>"$summary"
else
  echo "::error::vm/endpoint: TestZFSDiffReplay wrote no zfsdiff.log"; rc=1
fi
"""),
])

edit('ci/real-zfs-weights.txt', [
("""TestNFSReplay 5.00
""",
"""TestNFSReplay 5.00
# TestZFSDiffReplay replays zfsdiff.PRSample, 20 of the 100 corpus
# sequences (244 of 1,228 zfs invocations; the whole corpus was estimated
# at 15-30s with 4 workers, so ~3-6s). It stops starting sequences at 12s
# and fails past 15s. The whole corpus replays nightly (ZFSDIFF_FULL=1).
# Sharded with the NFS tests: its share sequences rewrite zfs.exports.
TestZFSDiffReplay 5.00
"""),
])
