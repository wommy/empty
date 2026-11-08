import os
import re
import shutil
import socket
import subprocess
import sys
import threading
import time

S = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/setup-go-test"
ACTION = "/home/user/empty/.claude/worktrees/agent-a20ec87c70404cd54/.github/actions/setup-go/action.yml"

text = open(ACTION, encoding="utf-8").read()
body = re.search(r"bash -c '\n(.*?)\n        ' _ ", text, re.S).group(1)
# test-only: shorten the timeouts so the hang case runs in seconds
assert body.count("--max-time 45") == 1 and body.count("--max-time 60") == 1
test_body = body.replace("--max-time 45", "--max-time 3").replace("--max-time 60", "--max-time 5")

# listener that accepts TCP connections and never answers (a hung server)
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0))
srv.listen(16)
port = srv.getsockname()[1]
held = []


def accept_forever():
    while True:
        try:
            c, _ = srv.accept()
            held.append(c)  # keep open, never reply
        except OSError:
            return


threading.Thread(target=accept_forever, daemon=True).start()

fixtures = os.path.join(S, "fixtures")
proxy_zip = os.path.join(fixtures, "proxy-toolchain.zip")
import hashlib
with open(proxy_zip, "rb") as f:
    proxy_sha = hashlib.sha256(f.read()).hexdigest()

d = os.path.join(S, "case-H-hung-primary")
shutil.rmtree(d, ignore_errors=True)
rt = os.path.join(d, "rt")
home = os.path.join(d, "home")
os.makedirs(rt)
os.makedirs(home)
t0 = time.time()
with open(os.path.join(rt, "go.log"), "w") as log:
    r = subprocess.run(
        ["bash", "-c", test_body, "_", rt, "http://127.0.0.1:%d/go.tgz" % port,
         "0" * 64, os.path.join(home, "sdk", "go"), "file://" + proxy_zip, proxy_sha],
        env={"PATH": os.environ["PATH"], "HOME": home}, stdout=log, stderr=subprocess.STDOUT,
        timeout=120)
elapsed = time.time() - t0
status = open(os.path.join(rt, "go.status")).read().strip()
log_text = open(os.path.join(rt, "go.log")).read()
print("elapsed %.1fs rc %d status %s done %s" % (elapsed, r.returncode, status,
      os.path.exists(os.path.join(rt, "go.done"))))
print(log_text)
version = open(os.path.join(home, "sdk", "go", "VERSION")).read().strip()
print("VERSION", version)
ok = (status == "ok" and "primary Go download failed; trying proxy.golang.org" in log_text
      and version == "go1.27.3")
print("HANG CASE", "PASS" if ok else "FAIL")
sys.exit(0 if ok else 1)
