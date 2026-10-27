#!/usr/bin/env bash
# setup-go's background download/verify/extract + install wait, with a file:// tarball
set -uo pipefail
cw=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave13/cw
t=$cw/gotest; rm -rf "$t"; mkdir -p "$t/src/go/bin" "$t/rt" "$t/home"
printf '#!/bin/sh\necho go version fake\n' >"$t/src/go/bin/go"; chmod +x "$t/src/go/bin/go"
tar -czf "$t/go.tgz" -C "$t/src/go" .
good=$(sha256sum "$t/go.tgz" | cut -c1-64)
for case in ok bad; do
  RUNNER_TEMP=$t/rt; HOME=$t/home; GO_URL=file://$t/go.tgz
  GO_SHA256=$good; [ $case = bad ] && GO_SHA256=0000000000000000000000000000000000000000000000000000000000000000
  rm -rf "$HOME/sdk"
  # --- step 1 (verbatim body)
  rm -f "$RUNNER_TEMP/go.done" "$RUNNER_TEMP/go.status"
  setsid nohup bash -c '
    if curl -fsSL --retry 3 -o "$1/go.tgz" "$2" &&
       echo "$3  $1/go.tgz" | sha256sum -c - &&
       mkdir -p "$4" && tar -xzf "$1/go.tgz" -C "$4"; then
      echo ok >"$1/go.status"
    else
      echo fail >"$1/go.status"
    fi
    touch "$1/go.done"
  ' _ "$RUNNER_TEMP" "$GO_URL" "$GO_SHA256" "$HOME/sdk/go" >"$RUNNER_TEMP/go.log" 2>&1 &
  # --- step 3 (body)
  (
    for _ in $(seq 240); do [ -e "$RUNNER_TEMP/go.done" ] && break; sleep 0.25; done
    cat "$RUNNER_TEMP/go.log" 2>/dev/null || true
    if [ "$(cat "$RUNNER_TEMP/go.status" 2>/dev/null)" != ok ]; then echo "::error::failed"; exit 1; fi
    "$HOME/sdk/go/bin/go" version
  )
  echo "case $case -> rc=$?"
done
