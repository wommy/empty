#!/bin/bash
# Local simulation of the buf download and install steps, using file:// URLs.
set -u
T=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/buf-test
W=$T/work
rm -rf "$W"
mkdir -p "$W/src/good/package/bin" "$W/src/bad/package/bin"
printf '#!/bin/sh\necho buf-fake-good\n' > "$W/src/good/package/bin/buf"
printf 'fake lint\n' > "$W/src/good/package/bin/protoc-gen-buf-lint"
printf '{"name":"@bufbuild/buf-linux-x64"}\n' > "$W/src/good/package/package.json"
printf '#!/bin/sh\necho buf-fake-BAD\n' > "$W/src/bad/package/bin/buf"
(cd "$W/src/good" && tar -czf "$W/good.tgz" package)
(cd "$W/src/bad" && tar -czf "$W/bad.tgz" package)
GOOD_SHA=$(sha256sum "$W/good.tgz" | cut -d' ' -f1)
BAD_SHA=$(sha256sum "$W/bad.tgz" | cut -d' ' -f1)
echo "good=$GOOD_SHA bad=$BAD_SHA"
BG=$(cat "$T/bg_body.sh")
INST=$T/install.sh

run_case() { # name primary_url pinned_sha fallback_url fallback_sha
  local name=$1 purl=$2 psha=$3 furl=$4 fsha=$5
  local rt="$W/$name/rt" home="$W/$name/home"
  mkdir -p "$rt" "$home"
  rm -f "$rt/buf.tgz.done" "$rt/buf.status"
  bash -c "$BG" _ "$rt" "$purl" "$psha" "$furl" "$fsha" > "$rt/buf.log" 2>&1
  echo "--- $name: status=$(cat "$rt/buf.status" 2>/dev/null) done=$([ -e "$rt/buf.tgz.done" ] && echo yes || echo no)"
  echo "    log: $(tr '\n' '|' < "$rt/buf.log")"
  RUNNER_TEMP=$rt HOME=$home GITHUB_PATH=$rt/gp bash -eo pipefail "$INST" > "$rt/inst.log" 2>&1
  local rc=$?
  echo "    install rc=$rc: $(tr '\n' '|' < "$rt/inst.log")"
  echo "    buf present: $([ -e "$home/buf/bin/buf" ] && echo yes || echo no); GITHUB_PATH: $(cat "$rt/gp" 2>/dev/null)"
}

GOODURL=file://$W/good.tgz
BADURL=file://$W/bad.tgz
MISSING=file://$W/nope.tgz
run_case a_primary_missing "$MISSING" "$GOOD_SHA" "$GOODURL" "$GOOD_SHA"
run_case b_primary_bad_sha "$BADURL" "$GOOD_SHA" "$GOODURL" "$GOOD_SHA"
run_case c_both_bad "$MISSING" "$GOOD_SHA" "$BADURL" "$GOOD_SHA"
run_case d_good_primary "$GOODURL" "$GOOD_SHA" "$MISSING" "$GOOD_SHA"
