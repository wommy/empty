#!/usr/bin/env python3
# Mutation runner: apply one mutant at a time, run the named tests, restore.
# usage: mutants.py <repo> <mutants.json> <out.txt>
import json, subprocess, sys, os

repo, spec, out = sys.argv[1], sys.argv[2], sys.argv[3]
mutants = json.load(open(spec))
log = open(out, "w")
for m in mutants:
    path = os.path.join(repo, m["file"])
    orig = open(path).read()
    assert orig.count(m["old"]) == 1, (m["name"], orig.count(m["old"]))
    try:
        open(path, "w").write(orig.replace(m["old"], m["new"]))
        r = subprocess.run(["go", "test", "-count=1", "-run", m["run"]] + m["pkgs"],
                           cwd=repo, capture_output=True, text=True)
        res = "KILLED" if r.returncode != 0 else "SURVIVED"
        log.write(f"=== {m['name']}: {res}\n-- mutation in {m['file']}:\n- {m['old']}\n+ {m['new']}\n")
        fails = [l for l in r.stdout.splitlines() if l.lstrip().startswith("--- FAIL") or "FAIL\t" in l or ".go:" in l]
        log.write("\n".join(fails[:40]) + "\n\n")
        print(m["name"], res)
    finally:
        open(path, "w").write(orig)
log.close()
