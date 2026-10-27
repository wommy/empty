import json, sys, csv, collections
from datetime import datetime

def ts(s):
    s = s.rstrip('Z')
    if '.' in s:
        a, b = s.split('.')
        s = a + '.' + b[:6]
    return datetime.fromisoformat(s)

label = sys.argv[1]
ev = []
for line in open(label + '.json', encoding='utf-8', errors='replace'):
    line = line.strip()
    if not line.startswith('{'):
        continue
    try:
        ev.append(json.loads(line))
    except Exception:
        pass

t0 = min(ts(e['Time']) for e in ev if 'Time' in e)
tests = {}     # (pkg,test) -> dict
pkgs = {}
paused = set(); conts = set()
firstev = {}; lastev = {}
for e in ev:
    a = e.get('Action'); p = e.get('Package'); t = e.get('Test')
    if p and 'Time' in e:
        tt = ts(e['Time'])
        firstev.setdefault(p, tt); lastev[p] = tt
    if a in ('pass', 'fail', 'skip'):
        if t:
            tests[(p, t)] = dict(pkg=p, test=t, status=a, sec=e.get('Elapsed', 0.0))
        else:
            pkgs[p] = dict(pkg=p, status=a, sec=e.get('Elapsed', 0.0),
                           start=(firstev[p] - t0).total_seconds(),
                           end=(ts(e['Time']) - t0).total_seconds())
    if a == 'pause' and t: paused.add((p, t))
    if a == 'cont' and t: conts.add((p, t))

short = lambda p: p.replace('github.com/wommy/empty/', '')
with open(label + '-tests.csv', 'w', newline='') as f:
    w = csv.writer(f)
    w.writerow(['package', 'test', 'level', 'status', 'seconds', 'parallel'])
    for (p, t), d in sorted(tests.items(), key=lambda kv: -kv[1]['sec']):
        w.writerow([short(p), t, 'sub' if '/' in t else 'top', d['status'], d['sec'], 'Y' if (p, t) in paused else 'N'])
with open(label + '-packages.csv', 'w', newline='') as f:
    w = csv.writer(f)
    w.writerow(['package', 'status', 'pkg_elapsed_s', 'first_event_s_from_t0', 'end_s_from_t0',
                'top_tests', 'subtests', 'top_sum_s', 'top_parallel', 'top_nonparallel_sum_s', 'top_skips'])
    for p, d in sorted(pkgs.items(), key=lambda kv: -kv[1]['sec']):
        top = [x for (pp, t), x in tests.items() if pp == p and '/' not in t]
        sub = [x for (pp, t), x in tests.items() if pp == p and '/' in t]
        par = [x for x in top if (p, x['test']) in paused]
        w.writerow([short(p), d['status'], d['sec'], round(d['start'], 2), round(d['end'], 2), len(top), len(sub),
                    round(sum(x['sec'] for x in top), 2), len(par),
                    round(sum(x['sec'] for x in top if (p, x['test']) not in paused), 2),
                    sum(1 for x in top if x['status'] == 'skip')])
print(label, 'pkgs', len(pkgs), 'tests', len(tests), 'paused', len(paused))
print('%-34s %7s %6s %6s %9s %5s %5s' % ('pkg', 'elapsed', 'first', 'end', 'topsum', 'ntop', 'npar'))
for p, d in sorted(pkgs.items(), key=lambda kv: -kv[1]['sec']):
    top = [x for (pp, t), x in tests.items() if pp == p and '/' not in t]
    print('%-34s %7.2f %6.1f %6.1f %9.2f %5d %5d' % (short(p), d['sec'], d['start'], d['end'], sum(x['sec'] for x in top), len(top), sum(1 for x in top if (p, x['test']) in paused)))
print('top 30')
for (p, t), d in sorted(tests.items(), key=lambda kv: -kv[1]['sec'])[:30]:
    print('%7.2f %-5s %s %s' % (d['sec'], d['status'], short(p), t))
