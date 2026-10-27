import json, sys
rows = []
for line in open(sys.argv[1]):
    try:
        e = json.loads(line)
    except Exception:
        continue
    if e.get('Action') in ('pass', 'fail', 'skip') and 'Test' in e and '/' not in e['Test']:
        rows.append((e.get('Elapsed', 0), e['Package'].replace('github.com/wommy/empty/', ''), e['Test'], e['Action']))
    if e.get('Action') in ('pass', 'fail') and 'Test' not in e:
        print('PKG', e['Package'].replace('github.com/wommy/empty/', ''), e.get('Elapsed'), e['Action'])
rows.sort(reverse=True)
n = int(sys.argv[2]) if len(sys.argv) > 2 else 40
for r in rows[:n]:
    print(*r)
print('tests', len(rows), 'sum', round(sum(r[0] for r in rows), 2))
