#!/usr/bin/env bash
# jlog.sh <saved get_job_logs json> <outdir> <note>: job log -> shard logs -> test-times.tsv, plus a step timeline
set -euo pipefail
repo=/home/user/empty/.claude/worktrees/agent-a53361ecab5fd8058
json=$1 dir=$2 note=$3
mkdir -p "$dir"
python3 -I - "$json" "$dir" <<'EOF'
import json,re,sys
src,d=sys.argv[1],sys.argv[2]
c=json.load(open(src))["logs_content"]
open(d+"/job.log","w").write(c)
fh=None
for l in c.split("\n"):
    body=l[29:]
    m=re.match(r'##\[group\](\S+\.log)$',body)
    if m: fh=open(d+"/"+m.group(1),"w"); continue
    if body.startswith('##[endgroup]'):
        if fh: fh.close()
        fh=None; continue
    if fh: fh.write(body+"\n")
EOF
{
  echo "# ci/test-times.awk over $note, reconstructed from the job log"
  echo "# nproc=4 mode=pr"
  for s in endpoint.0 endpoint.1 endpoint.2 csi-other csi-mount csi-block; do
    mawk -v shard="$s" -f "$repo/ci/test-times.awk" "$dir/$s.log"
  done
} >"$dir/test-times.tsv"
grep -E '##\[group\]Run |warm guest|status: disabled|start at uptime|end at uptime| rc=[0-9]|Cleaning up orphan|zfsdiff replay: .* in |Cache Size' "$dir/job.log" | grep -v '\[36;1m' | cut -c1-150
grep -v '^#' "$dir/test-times.tsv" | sort -t "$(printf '\t')" -k6,6gr | head -8
