#!/usr/bin/env bash
# split the J3 job log into shard logs and run ci/test-times.awk over them
set -euo pipefail
here=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/cw
repo=/home/user/empty/.claude/worktrees/agent-a53361ecab5fd8058
cd "$here"
python3 -I - <<'EOF'
import re,os
os.makedirs('j3',exist_ok=True)
fh=None
for l in open('vm-zfsdiff.log'):
    body=l.rstrip('\n')[29:]
    m=re.match(r'##\[group\](\S+\.log)$',body)
    if m: fh=open('j3/'+m.group(1),'w'); continue
    if body.startswith('##[endgroup]'):
        if fh: fh.close()
        fh=None; continue
    if fh: fh.write(body+'\n')
EOF
{
  echo "# ci/test-times.awk over run 40469622947 (job 122073313593), reconstructed from the job log"
  echo "# nproc=4 mode=pr"
  for s in endpoint.0 endpoint.1 endpoint.2 csi-other csi-mount csi-block; do
    mawk -v shard="$s" -f "$repo/ci/test-times.awk" "j3/$s.log"
  done
} >j3/test-times.tsv
wc -l j3/test-times.tsv
grep -v '^#' j3/test-times.tsv | sort -t "$(printf '\t')" -k6,6gr | head -12
