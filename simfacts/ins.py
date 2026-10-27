import sys
# ins.py FILE SNIPPET: insert SNIPPET (which ends the facts slice) in place
# of the facts slice's closing brace.
p, snip = sys.argv[1], sys.argv[2]
s = open(p).read()
ins = open(snip).read()
old = '\t}},\n}\n\nfunc TestRealFacts'
if s.count(old) == 1:
    s = s.replace(old, '\t}},\n' + ins + '\nfunc TestRealFacts')
else:
    # facts slice already followed by availNear: insert before its closing
    old = '\t\t}},\n}\n\n// availNear'
    assert s.count(old) == 1, 'anchor'
    s = s.replace(old, '\t\t}},\n' + ins + '}\n\n// availNear')
open(p, 'w').write(s)
