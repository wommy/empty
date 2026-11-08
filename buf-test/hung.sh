#!/bin/bash
# Case (e): primary is a local listener that accepts and never replies.
# The 45s bounds are shrunk to 3s in a copy of the body, for this test only.
set -u
T=/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/buf-test
W=$T/work/e_hung
rm -rf "$W"
mkdir -p "$W/src/good/package/bin" "$W/rt" "$W/home"
printf '#!/bin/sh\necho buf-fake-good\n' > "$W/src/good/package/bin/buf"
printf '{"name":"x"}\n' > "$W/src/good/package/package.json"
(cd "$W/src/good" && tar -czf "$W/good.tgz" package)
GOOD_SHA=$(sha256sum "$W/good.tgz" | cut -d' ' -f1)
PORT=18765
python3 -I -c '
import socket, time, sys
s = socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", int(sys.argv[1]))); s.listen(16)
conns = []
end = time.time() + 40
while time.time() < end:
    s.settimeout(1.0)
    try:
        c, _ = s.accept(); conns.append(c)
    except socket.timeout:
        pass
' "$PORT" &
LISTENER=$!
sleep 1
sed -e 's/timeout 45 /timeout 3 /g' -e 's/--max-time 45 /--max-time 3 /g' "$T/bg_body.sh" > "$W/bg_short.sh"
grep -c 'timeout 3 ' "$W/bg_short.sh"
start=$(date +%s)
NO_PROXY=127.0.0.1,localhost no_proxy=127.0.0.1,localhost \
  bash -c "$(cat "$W/bg_short.sh")" _ "$W/rt" "http://127.0.0.1:$PORT/buf.tgz" "$GOOD_SHA" "file://$W/good.tgz" "$GOOD_SHA" > "$W/rt/buf.log" 2>&1
elapsed=$(( $(date +%s) - start ))
echo "elapsed=${elapsed}s (primary bound 3s + fallback)"
echo "status=$(cat "$W/rt/buf.status" 2>/dev/null) done=$([ -e "$W/rt/buf.tgz.done" ] && echo yes || echo no)"
echo "log: $(tr '\n' '|' < "$W/rt/buf.log")"
kill $LISTENER 2>/dev/null
wait $LISTENER 2>/dev/null
RUNNER_TEMP=$W/rt HOME=$W/home GITHUB_PATH=$W/rt/gp bash -eo pipefail "$T/install.sh" > "$W/rt/inst.log" 2>&1
echo "install rc=$?; buf file: $(ls "$W/home/buf/bin/" 2>/dev/null | tr '\n' ' ')"
