import hashlib
import os
import shutil
import subprocess
import sys
import tarfile

S = "/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/setup-go-test"
src = os.path.join(S, "primary-src")
shutil.rmtree(src, ignore_errors=True)
os.makedirs(os.path.join(src, "go"))
with open(os.path.join(src, "go", "VERSION"), "w") as f:
    f.write("go1.27.3\n")
tgz = os.path.join(S, "fixtures", "good-primary.tgz")
with tarfile.open(tgz, "w:gz") as t:
    t.add(os.path.join(src, "go"), arcname="go")
with open(tgz, "rb") as f:
    sha = hashlib.sha256(f.read()).hexdigest()
with open(os.path.join(S, "body.sh")) as f:
    body = f.read()

d = os.path.join(S, "case-P-primary-ok")
shutil.rmtree(d, ignore_errors=True)
rt = os.path.join(d, "rt")
home = os.path.join(d, "home")
os.makedirs(rt)
os.makedirs(home)
with open(os.path.join(rt, "go.log"), "w") as log:
    subprocess.run(
        ["bash", "-c", body, "_", rt, "file://" + tgz, sha,
         os.path.join(home, "sdk", "go"), "file:///nonexistent.zip", "0" * 64],
        env={"PATH": os.environ["PATH"], "HOME": home},
        stdout=log, stderr=subprocess.STDOUT)
with open(os.path.join(rt, "go.status")) as f:
    print("status:", f.read().strip())
with open(os.path.join(rt, "go.log")) as f:
    print("log:", repr(f.read()))
with open(os.path.join(home, "sdk", "go", "go", "VERSION")) as f:
    print("VERSION:", f.read().strip())
