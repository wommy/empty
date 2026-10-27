import csv, collections, sys
short = lambda s: s
def load(label):
    d = {}
    for r in csv.DictReader(open(label + '-tests.csv')):
        d[(r['package'], r['test'])] = (float(r['seconds']), r['status'], r['level'])
    return d

def agg(name, labels):
    runs = [load(l) for l in labels]
    keys = set().union(*[r.keys() for r in runs])
    rows = []
    for k in keys:
        vals = [r[k][0] for r in runs if k in r]
        st = [r[k][1] for r in runs if k in r][0]; lv = [r[k][2] for r in runs if k in r][0]
        rows.append((k[0], k[1], lv, st, min(vals), sorted(vals)[len(vals)//2], max(vals), len(vals)))
    rows.sort(key=lambda r: -r[4])
    with open(name + '-per-test.csv', 'w', newline='') as f:
        w = csv.writer(f)
        w.writerow(['package', 'test', 'level', 'status', 'min_s', 'median_s', 'max_s', 'runs', 'parallel'])
        for r in rows:
            w.writerow(list(r) + ['N'])  # no test calls t.Parallel (0 'pause' events in every run)
    pk = collections.defaultdict(lambda: [0.0, 0, 0.0])
    for r in rows:
        if r[2] == 'top':
            pk[r[0]][0] += r[4]; pk[r[0]][1] += 1; pk[r[0]][2] += r[5]
    with open(name + '-per-package.csv', 'w', newline='') as f:
        w = csv.writer(f)
        w.writerow(['package', 'top_level_tests', 'sum_of_min_s', 'sum_of_median_s', 'parallel_tests'])
        for p, (s, n, m) in sorted(pk.items(), key=lambda kv: -kv[1][0]):
            w.writerow([p, n, round(s, 2), round(m, 2), 0])
    print(name, 'total top-level sum of min', round(sum(v[0] for v in pk.values()), 2), 'n', sum(v[1] for v in pk.values()))
    for p, (s, n, m) in sorted(pk.items(), key=lambda kv: -kv[1][0]):
        print('  %-24s n=%3d min-sum=%6.2f median-sum=%6.2f' % (p, n, s, m))
    print('  top 12 by min:')
    for r in rows[:12]:
        print('   %6.2f (med %6.2f max %6.2f) %s %s' % (r[4], r[5], r[6], r[0], r[1]))

agg('plain', ['plain-warm', 'plain-w1', 'plain-w2', 'plain-w3'])
agg('race', ['race-warm', 'race-w1', 'race-w2'])
