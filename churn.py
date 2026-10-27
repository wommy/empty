import sys,collections,re
fix=collections.Counter();all_=collections.Counter();msgs=0;fixmsgs=0;isfix=False
for l in sys.stdin:
    if l.startswith("@@"):
        cur=l[2:].lower();msgs+=1;isfix=bool(re.search(r"\bfix|bug|race|leak|orphan|stale|retry|robust|regress|support|scale|25\.",cur));fixmsgs+=isfix;continue
    p=l.split("\t")
    if len(p)==3 and p[0].isdigit():
        f=p[2].strip();k="/".join(f.split("/")[:3]) if f.startswith("src/driver") else "/".join(f.split("/")[:2])
        n=int(p[0])+int(p[1]);all_[k]+=n
        if isfix: fix[k]+=n
print("commits:",msgs,"fix/compat-ish:",fixmsgs)
for k,v in all_.most_common(14): print(f"{k:42}{v:7}{fix[k]:7}")
