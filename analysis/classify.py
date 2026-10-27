import re, sys
path = sys.argv[1]
lines = open(path, encoding="utf-8").read().splitlines()
in_block = False
cats = {"control": 0, "payload": 0, "call": 0, "other": 0, "comment/blank": 0}
ctrl = re.compile(r"^\s*(if|else|switch|case|for|while|return|throw|try|catch|break|continue|default:)\b|\}\s*else\b|\?\s*.+:")
payload_kv = re.compile(r"^\s*[\"']?[A-Za-z_$][\w$.\-]*[\"']?\s*:\s*[^:]*,?\s*$|^\s*[\"'][^\"']+[\"']\s*,?\s*$")
structural = re.compile(r"^\s*[\]\}\)\[\{]+[,;)]*\s*$|^\s*\{\s*$|^\s*\]\s*,?\s*$")
call = re.compile(r"\bawait\s+(this|httpClient|httpApiClient|driver|sshClient|client|[\w.]+)\.\w+\(|\bawait\s+this\.\w+\(")
for raw in lines:
    s = raw.strip()
    if in_block:
        if "*/" in s: in_block = False
        cats["comment/blank"] += 1; continue
    if s.startswith("/*"):
        if "*/" not in s: in_block = True
        cats["comment/blank"] += 1; continue
    if not s or s.startswith("//"):
        cats["comment/blank"] += 1; continue
    if ctrl.search(raw) and not re.search(r"\}\s*$", s[:0]):
        cats["control"] += 1
    elif structural.match(raw) or payload_kv.match(raw):
        cats["payload"] += 1
    elif call.search(raw):
        cats["call"] += 1
    else:
        cats["other"] += 1
code = sum(v for k, v in cats.items() if k != "comment/blank")
print(path, "total lines", len(lines))
for k, v in cats.items():
    print(f"  {k:14s} {v:5d}  {100*v/code:5.1f}% of code lines" if k!="comment/blank" else f"  {k:14s} {v:5d}")
