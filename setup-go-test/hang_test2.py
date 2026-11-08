import hashlib
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
SCALED_WAIT = 120 * 3 / 45 * 0 + 8.5  # real 120s wait scaled by the same factor as the timeouts (~15x)

text = open(ACTION, encoding="utf-8").read()
body = re.search(r"bash -c '\n(.*?)\n        ' _ ", text, re.S).group(1)
subs = [("timeout 45 curl", "timeout 3 curl"), ("--max-time 45", "--max-time 3"),
        ("timeout 60 curl", "timeout 5 curl"), ("--max-time 60", "--max-time 5")]
test_body = body
for old, new in subs:
    assert test_body.count(old) == 1, old
    test_body = test_body.replace(old, new)

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
            held.append(c)
        except OSError:
            return


threading.Thread(target=accept_forever, daemon=True).start()
hung = "http://127.0.0.1:%d/x" % port

proxy_zip = os.path.join(S, "fixtures", "proxy-toolchain.zip")
with open(proxy_zip, "rb") as f:
    proxy_sha = hashlib.sha256(f.read()).hexdigest()


def case(name, primary, proxy, psha):
    d = os.path.join(S, "case-" + name)
    shutil.rmtree(d, ignore_errors=True)
    rt = os.path.join(d, "rt")
    home = os.path.join(d, "home")
    os.makedirs(rt)
    os.makedirs(home)
    t0 = time.time()
    with open(os.path.join(rt, "go.log"), "w") as log:
        subprocess.run(
            ["bash", "-c", test_body, "_", rt, primary, "0" * 64,
             os.path.join(home, "sdk", "go"), proxy, psha],
            env={"PATH": os.environ["PATH"], "HOME": home},
            stdout=log, stderr=subprocess.STDOUT, timeout=120)
    elapsed = time.time() - t0
    st = os.path.join(rt, "go.status")
    status = open(st).read().strip() if os.path.exists(st) else "<missing>"
    done = os.path.exists(os.path.join(rt, "go.done"))
    log_text = open(os.path.join(rt, "go.log")).read()
    vpath = os.path.join(home, "sdk", "go", "VERSION")
    version = open(vpath).read().strip() if os.path.exists(vpath) else "<none>"
    print("---- %s: elapsed %.1fs status=%s done=%s VERSION=%s" % (name, elapsed, status, done, version))
    print(log_text)
    return status, done, log_text, version, elapsed


r = {}
s, dn, log, ver, el = case("H1-hung-primary-good-fallback", hung, "file://" + proxy_zip, proxy_sha)
r["H1"] = s == "ok" and dn and "trying proxy.golang.org" in log and ver == "go1.27.3" and el < SCALED_WAIT
s, dn, log, ver, el = case("H2-hung-primary-hung-fallback", hung, hung, "0" * 64)
r["H2"] = s == "fail" and dn and el < SCALED_WAIT
print("\nsummary", {k: bool(v) for k, v in r.items()})
sys.exit(0 if all(r.values()) else 1)
