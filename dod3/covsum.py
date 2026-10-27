import sys,collections
# merge profiles; blocks keyed by file:pos; count stmt
def load(fs):
    cov={}
    for f in fs:
        for l in open(f):
            if l.startswith('mode:'):continue
            k,n,c=l.rsplit(' ',2); n=int(n);c=int(c)
            prev=cov.get(k,(n,0)); cov[k]=(n,max(prev[1],c))
    return cov
cov=load(sys.argv[1].split(','))
tot=collections.defaultdict(lambda:[0,0])
for k,(n,c) in cov.items():
    f=k.split(':')[0].replace('github.com/wommy/empty/','')
    tot[f][0]+=n; tot[f][1]+=n if c>0 else 0
T=[0,0]
for f in sorted(tot):
    a,b=tot[f]; T[0]+=a;T[1]+=b
    print(f"{f:45s} {b:4d}/{a:4d} {100*b/a:5.1f}%")
print(f"TOTAL {T[1]}/{T[0]} {100*T[1]/T[0]:.1f}%")
