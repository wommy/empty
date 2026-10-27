import json, collections, sys
d = json.load(open('/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad/wave8/spec.json'))
r = d['rows']
print(d.keys())
print(json.dumps(r[0], indent=1))
keys = collections.Counter(k for x in r for k in x)
print(keys)
