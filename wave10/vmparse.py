import re, csv
rows = []
leg = None
pat = re.compile(r'^(\S+Z) (\s*)--- (PASS|FAIL|SKIP): (\S+) \(([\d.]+)s\)')
for i, line in enumerate(open('vm-job.log', encoding='utf-8'), 1):
    line = line.rstrip('\n')
    if i <= 420 or i > 1056:
        continue
    leg = 'endpoint' if i <= 865 else ('csi-mount' if i <= 967 else 'csi-block')
    m = pat.match(line)
    if m:
        ts, ind, st, name, sec = m.groups()
        rows.append((leg, name, 'sub' if ind else 'top', st, float(sec), ts))
with open('vm-per-test.csv', 'w', newline='') as f:
    w = csv.writer(f)
    w.writerow(['leg', 'test', 'level', 'status', 'seconds', 'log_ts'])
    for r in sorted(rows, key=lambda r: -r[4]):
        w.writerow(r)
from collections import defaultdict
tot = defaultdict(float); cnt = defaultdict(int); skips = defaultdict(int)
for leg, name, lvl, st, sec, ts in rows:
    if lvl == 'top':
        tot[leg] += sec; cnt[leg] += 1
        if st == 'SKIP': skips[leg] += 1
for l in tot: print(l, 'top-level sum', round(tot[l], 2), 'n', cnt[l], 'skips', skips[l])
print('top 25')
for r in sorted(rows, key=lambda r: -r[4])[:25]: print(r[:5])
