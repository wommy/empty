#!/usr/bin/env bash
# fetch ci.yml runs and their jobs as JSON into raw/, then build timings.csv
set -euo pipefail
cd "$(dirname "$0")/.."
mkdir -p raw
gh api 'repos/wommy/empty/actions/workflows/ci.yml/runs?per_page=30' >raw/runs.json
for id in $(python3 -I -c 'import json;print(" ".join(str(r["id"]) for r in json.load(open("raw/runs.json"))["workflow_runs"] if r["run_number"]>=3))'); do
  gh api "repos/wommy/empty/actions/runs/$id/jobs?per_page=50" >"raw/jobs-$id.json"
done
python3 -I scripts/tocsv.py raw timings.csv
