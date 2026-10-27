import sys, json, datetime as dt
src = sys.argv[1]
def t(s): return None if s=='-' else dt.datetime.strptime(s,'%H:%M:%S')
runs=[]; cur=None
for line in open(src):
    line=line.strip()
    if not line or line.startswith('#'): continue
    f=line.split()
    if f[0]=='RUN':
        cur=dict(num=int(f[1]),id=int(f[2]),sha=f[3],concl=f[4],created=t(f[5]),updated=t(f[6]),jobs={}); runs.append(cur)
    else:
        name=f[0]; ts=[t(x) for x in f[1:]]
        j=dict(start=ts[0],end=ts[1])
        for k,i in (('restore',2),('setupgo',4),('test',6),('nfs',8)):
            if len(ts)>i+1 and ts[i]: j[k]=(ts[i],ts[i+1])
        cur['jobs'][name]=j
def d(a,b): return int((b-a).total_seconds())
rows=[]
for r in runs:
    c=r['created']; js=r['jobs']
    row=dict(run=r['num'],run_id=r['id'],sha=r['sha'],conclusion=r['concl'],total=d(c,r['updated']),
             queue_first=min(d(c,j['start']) for j in js.values()),queue_last=max(d(c,j['start']) for j in js.values()))
    crit=max(js,key=lambda n:js[n]['end']); row['critical']=crit
    for n,j in js.items():
        row[n+'_wall']=d(j['start'],j['end']); row[n+'_end']=d(c,j['end']); row[n+'_queue']=d(c,j['start'])
        for k in ('restore','setupgo','test','nfs'):
            if k in j: row[n+'_'+k]=d(*j[k])
    rows.append(row)
mode=sys.argv[2] if len(sys.argv)>2 else 'md'
if mode=='jsonl':
    for row in rows: print(json.dumps(row))
else:
    print('| run | sha | total | queue first/last | crit | ep0 q/wall (restore,setupgo,test) | ep1 q/wall (r,g,t) | csi q/wall (r,g,t) | test wall (g, sim, nfs) | static wall (g) |')
    print('|---|---|---|---|---|---|---|---|---|---|')
    for x in rows:
        def vm(n): return f"{x[n+'_queue']}/{x[n+'_wall']} ({x.get(n+'_restore')},{x.get(n+'_setupgo')},{x.get(n+'_test')})"
        print(f"| {x['run']} | {x['sha']}{'' if x['conclusion']=='success' else ' ('+x['conclusion']+')'} | {x['total']} | {x['queue_first']}/{x['queue_last']} | {x['critical']} | {vm('ep0')} | {vm('ep1')} | {vm('csi')} | {x['test_wall']} ({x['test_setupgo']}, {x['test_test']}, {x.get('test_nfs','-')}) | {x['static_wall']} ({x['static_setupgo']}) |")
    import statistics as st
    for k in ['total','queue_first','queue_last','ep0_restore','ep1_restore','csi_restore','ep0_test','ep1_test','csi_test','test_test','ep0_setupgo','ep1_setupgo','csi_setupgo','test_setupgo']:
        v=[x[k] for x in rows if k in x]
        print(k, 'min',min(v),'med',st.median(v),'max',max(v),'vals',v)
