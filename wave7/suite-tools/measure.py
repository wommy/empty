#!/usr/bin/env python3
"""Time suite phases: wall and CPU (user+sys of children), cold vs warm cache.

usage: measure.py <repo copy> <label> <reps> [phase ...]
Prints one JSON line per (phase, rep). The host is shared, so CPU seconds are
the stable number; wall is reported too.
"""
import json, os, resource, shutil, subprocess, sys, tempfile, time

repo, label, reps = os.path.abspath(sys.argv[1]), sys.argv[2], int(sys.argv[3])
only = set(sys.argv[4:])
base = dict(os.environ, GOTOOLCHAIN="go1.27.3")

def run(cmd, cache=None):
    env = dict(base)
    if cache:
        env["GOCACHE"] = cache
    r0 = resource.getrusage(resource.RUSAGE_CHILDREN)
    t0 = time.monotonic()
    p = subprocess.run(cmd, cwd=repo, env=env, shell=True, capture_output=True, text=True)
    wall = time.monotonic() - t0
    r1 = resource.getrusage(resource.RUSAGE_CHILDREN)
    cpu = (r1.ru_utime - r0.ru_utime) + (r1.ru_stime - r0.ru_stime)
    if p.returncode:
        sys.stderr.write(p.stdout[-3000:] + p.stderr[-3000:])
    return wall, cpu, p.returncode

phases = {
    # name: (command, cold?)
    "vet-cold": ("go vet ./...", True),
    "test-cold": ("go test -count=1 ./...", True),
    "race-cold": ("go test -race -count=1 ./...", True),
    "compile-cold": ("for p in $(go list ./... ); do go test -c -o /dev/null $p >/dev/null; done", True),
    "race-compile-cold": ("for p in $(go list ./... ); do go test -race -c -o /dev/null $p >/dev/null; done", True),
    "test-warm": ("go test -count=1 ./...", False),
    "race-warm": ("go test -race -count=1 ./...", False),
    "endpoint-warm": ("go test -count=1 ./internal/endpoint", False),
    "fuzz-overhead": ("go test -run '^$' -fuzz FuzzCreateConverges -fuzztime 1x ./internal/endpoint && "
                      "go test -run '^$' -fuzz FuzzParseColumns -fuzztime 1x ./internal/zfs", False),
}
for name, (cmd, cold) in phases.items():
    if only and name not in only:
        continue
    for i in range(reps):
        cache = None
        if cold:
            cache = tempfile.mkdtemp(prefix="gocache-")
        if not cold and i == 0:
            run(cmd)  # prime
        wall, cpu, rc = run(cmd, cache)
        if cache:
            shutil.rmtree(cache, ignore_errors=True)
        print(json.dumps(dict(label=label, phase=name, rep=i, wall=round(wall, 2), cpu=round(cpu, 2), rc=rc)), flush=True)
