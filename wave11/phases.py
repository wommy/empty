import re,sys
from datetime import datetime
def ts(l): return datetime.strptime(l[:26],"%Y-%m-%dT%H:%M:%S.%f")
for f in sys.argv[1:]:
    L=open(f).read().splitlines()
    marks={}
    def first(name,pat):
        for l in L:
            if re.search(pat,l): marks[name]=ts(l.lstrip('﻿')); return
    first("nfsd","nfsd threads")
    first("ep_first_run","=== RUN")
    first("suite1","Running Suite")
    for l in L:
        if "Running Suite" in l: marks["suite_last"]=ts(l)
    first("ep_done",r"^\S+ (PASS|ok)\s*$")
    for l in L:
        if re.search(r"Ran \d+ of \d+ Specs",l): marks.setdefault("ran",[]).append((ts(l),l[29:110]))
    first("step_end",r"##\[group\]Run (test -f|actions/cache/save)")
    print("==",f)
    for k,v in marks.items(): print(" ",k,v if not isinstance(v,list) else [(str(a.time()),b) for a,b in v])
    if "nfsd" in marks and "step_end" in marks: print("  nfsd->step end:",(marks["step_end"]-marks["nfsd"]).total_seconds())
