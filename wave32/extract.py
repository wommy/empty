import json, re, sys
for p in sys.argv[1:]:
    d = json.load(open(p))
    txt = d["logs_content"]
    print("== job", d.get("job_id"), p.split("-")[-1][:13])
    for line in txt.splitlines():
        if re.search(r"timeline: login prompt|part [01]/2: [0-9.]+ s|forks \d+ \(", line):
            print(line[:300])
