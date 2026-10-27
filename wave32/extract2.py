import json, re, sys
for p in sys.argv[1:]:
    d = json.load(open(p))
    L = d["logs_content"].splitlines()
    gl = [l for l in L if "guest load over the test phase:" in l]
    lp = [l for l in L if "timeline: login prompt" in l]
    fk = [l for l in L if re.search(r"forks \d+ \(", l)]
    print("== job", d["job_id"])
    print("  LP ", lp[0][:200])
    print("  GL ", gl[0][:200])
    print("  FK ", fk[0][:200])
