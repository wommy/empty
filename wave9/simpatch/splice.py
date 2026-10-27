import sys, re
path, newfile = sys.argv[1], sys.argv[2]
s = open(path).read()
new = open(newfile).read()
# split new into top-level funcs/vars by "\n// ==== " markers: each block starts with a header line "// ==== <signature-prefix>"
blocks = new.split("\n// ==== ")
for b in blocks[1:]:
    head, body = b.split("\n", 1)
    head = head.strip()
    if head.startswith("ADD "):
        anchor = head[4:]
        i = s.index(anchor)
        s = s[:i] + body.rstrip("\n") + "\n\n" + s[i:]
        continue
    # replace the declaration starting with head (including its preceding doc comment) up to the next top-level decl
    i = s.index(head)
    # back up over doc comment lines
    j = s.rfind("\n\n", 0, i) + 2
    k = i
    m = re.compile(r"\n(func |var |type |const |// )", re.M)
    # find end: next line starting at column 0 that begins a new decl or comment after a closing brace line
    end = s.find("\n}\n", i)
    end = end + 3
    s = s[:j] + body.rstrip("\n") + "\n" + s[end:]
open(path, "w").write(s)
