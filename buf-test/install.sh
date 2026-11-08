for _ in $(seq 200); do [ -e "$RUNNER_TEMP/buf.tgz.done" ] && break; sleep 0.5; done
cat "$RUNNER_TEMP/buf.log" 2>/dev/null || true # the download and sha256 verdicts
if [ "$(cat "$RUNNER_TEMP/buf.status" 2>/dev/null)" != ok ]; then
  echo "::error::buf download or sha256 check failed or did not finish"
  exit 1
fi
mkdir -p "$HOME/buf"
tar -xzf "$RUNNER_TEMP/buf.tgz" -C "$HOME/buf" --strip-components=1
echo "$HOME/buf/bin" >> "$GITHUB_PATH"
