import hashlib
import os
import re
import shutil
import stat
import subprocess
import sys
import zipfile

SCR = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/setup-go-test"
ACTION = "/home/user/empty/.claude/worktrees/agent-a20ec87c70404cd54/.github/actions/setup-go/action.yml"
PREFIX = "golang.org/toolchain@v0.0.1-go1.27.3.linux-amd64/"


def sha256(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


# 1. extract the bash -c body from the committed action.yml
text = open(ACTION, encoding="utf-8").read()
m = re.search(r"bash -c '\n(.*?)\n        ' _ ", text, re.S)
assert m, "could not locate bash -c body"
body = m.group(1)
body_path = os.path.join(SCR, "body.sh")
with open(body_path, "w") as f:
    f.write(body + "\n")
print("== bash -n on extracted body")
r = subprocess.run(["bash", "-n", body_path], capture_output=True, text=True)
print("rc", r.returncode, r.stderr.strip())
assert r.returncode == 0

# YAML parse of the whole file (pyyaml if present)
try:
    import yaml
    with open(ACTION) as f:
        doc = yaml.safe_load(f)
    steps = doc["runs"]["steps"]
    start = [s for s in steps if s.get("name") == "start Go download"][0]
    norm = lambda t: [l.strip() for l in t.splitlines() if l.strip()]
    assert norm(body) in [norm(start["run"])[i:i+len(norm(body))] for i in range(len(norm(start["run"])))], "body not in YAML run"
    print("== yaml: parsed; start-step run contains the extracted body")
except ImportError:
    print("== pyyaml missing; skipped YAML-level body check")

# 2. build the fake proxy zip with the golang.org/toolchain layout
zdir = os.path.join(SCR, "fixtures")
os.makedirs(zdir, exist_ok=True)
zip_path = os.path.join(zdir, "proxy-toolchain.zip")
with zipfile.ZipFile(zip_path, "w", zipfile.ZIP_DEFLATED) as z:
    def add(name, data, mode):
        info = zipfile.ZipInfo(PREFIX + name)
        info.external_attr = (mode | stat.S_IFREG) << 16
        info.compress_type = zipfile.ZIP_DEFLATED
        z.writestr(info, data)
    add("VERSION", "go1.27.3\n", 0o644)
    add("bin/go", "#!/bin/sh\necho fake go\n", 0o755)
    add("bin/gofmt", "#!/bin/sh\necho fake gofmt\n", 0o755)
    add("pkg/tool/README", "x\n", 0o644)
PROXY_SHA = sha256(zip_path)
print("== proxy zip sha256", PROXY_SHA)

# a garbage "primary" tgz (sha passes, tar fails) and a good-looking primary tgz
garbage = os.path.join(zdir, "garbage.tgz")
with open(garbage, "wb") as f:
    f.write(b"this is not a gzip tarball\n")
GARBAGE_SHA = sha256(garbage)

wrong = "0" * 64
NONEXISTENT = "file://" + os.path.join(SCR, "does-not-exist.tgz")


def run_case(name, primary_url, primary_sha, proxy_url, proxy_sha):
    d = os.path.join(SCR, "case-" + name)
    shutil.rmtree(d, ignore_errors=True)
    rt = os.path.join(d, "rt")
    home = os.path.join(d, "home")
    os.makedirs(rt)
    os.makedirs(home)
    env = {"PATH": os.environ["PATH"], "HOME": home, "RUNNER_TEMP": rt}
    with open(os.path.join(rt, "go.log"), "w") as log:
        r = subprocess.run(
            ["bash", "-c", body, "_", rt, primary_url, primary_sha,
             os.path.join(home, "sdk", "go"), proxy_url, proxy_sha],
            env=env, stdout=log, stderr=subprocess.STDOUT)
    status = open(os.path.join(rt, "go.status")).read().strip() if os.path.exists(os.path.join(rt, "go.status")) else "<missing>"
    done = os.path.exists(os.path.join(rt, "go.done"))
    goroot = os.path.join(home, "sdk", "go")
    log_text = open(os.path.join(rt, "go.log")).read()
    print("---- case:", name)
    print("bash rc:", r.returncode, "| go.status:", status, "| go.done:", done)
    print("go.log:")
    for line in log_text.splitlines():
        print("   |", line)
    if os.path.isdir(goroot):
        print("sdk/go tree (top level):", sorted(os.listdir(goroot)))
    else:
        print("sdk/go tree: <absent>")
    return status, done, goroot, log_text


results = {}

# A: primary unreachable, proxy good -> fallback succeeds
s, d, g, log = run_case("A-fallback-ok", NONEXISTENT, wrong,
                        "file://" + zip_path, PROXY_SHA)
results["A"] = (s == "ok" and d and "primary Go download failed; trying proxy.golang.org" in log
                and open(os.path.join(g, "VERSION")).read().strip() == "go1.27.3"
                and not os.path.exists(os.path.join(g, "golang.org"))
                and os.access(os.path.join(g, "bin", "go"), os.X_OK))

# B: primary unreachable, proxy wrong sha -> fail
s, d, g, log = run_case("B-proxy-wrong-sha", NONEXISTENT, wrong,
                        "file://" + zip_path, wrong)
results["B"] = (s == "fail" and d and "trying proxy.golang.org" in log)

# C: primary tgz with correct sha but corrupt; tar fails after mkdir -p $4;
#    fallback must remove that partial dir and install cleanly
s, d, g, log = run_case("C-partial-primary", "file://" + garbage, GARBAGE_SHA,
                        "file://" + zip_path, PROXY_SHA)
results["C"] = (s == "ok" and "trying proxy.golang.org" in log
                and open(os.path.join(g, "VERSION")).read().strip() == "go1.27.3"
                and not os.path.exists(os.path.join(g, "toolchain@v0.0.1-go1.27.3.linux-amd64"))
                and not os.path.exists(os.path.join(g, "golang.org")))

# D: primary wrong sha (file exists), proxy good -> fallback
s, d, g, log = run_case("D-primary-wrong-sha", "file://" + garbage, wrong,
                        "file://" + zip_path, PROXY_SHA)
results["D"] = (s == "ok" and "trying proxy.golang.org" in log
                and open(os.path.join(g, "VERSION")).read().strip() == "go1.27.3")

# E: both unreachable -> fail
s, d, g, log = run_case("E-both-fail", NONEXISTENT, wrong,
                        "file:///does-not-exist.zip", wrong)
results["E"] = (s == "fail" and d)

print("\n== summary")
for k in sorted(results):
    print(k, "PASS" if results[k] else "FAIL")
sys.exit(0 if all(results.values()) else 1)
