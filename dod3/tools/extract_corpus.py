#!/usr/bin/env python3
"""Mechanically extract every stderr-reachable error string from OpenZFS
libzfs + cmd/zfs/zfs_main.c and pair each with the EZFS_* code libzfs itself
assigns (the authoritative classification; stderr text is a lossy projection).

usage: extract_corpus.py <openzfs-root> <out.json>
"""
import json, re, sys, os

root, out = sys.argv[1], sys.argv[2]
FILES = sorted(
    [f"lib/libzfs/{f}" for f in os.listdir(f"{root}/lib/libzfs") if f.endswith(".c")]
    + [f"lib/libzfs/os/linux/{f}" for f in os.listdir(f"{root}/lib/libzfs/os/linux") if f.endswith(".c")]
    + ["cmd/zfs/zfs_main.c"])

def strip_comments(src):
    # keep newlines so line numbers survive; leave string/char literals intact
    o, i, n = [], 0, len(src)
    while i < n:
        c = src[i]
        if c == '"' or c == "'":
            j = i + 1
            while j < n and src[j] != c:
                j += 2 if src[j] == "\\" else 1
            o.append(src[i:j+1]); i = j + 1
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2); j = n if j < 0 else j + 2
            o.append(re.sub(r"[^\n]", " ", src[i:j])); i = j
        elif src.startswith("//", i):
            j = src.find("\n", i); j = n if j < 0 else j
            o.append(" " * (j - i)); i = j
        else:
            o.append(c); i += 1
    return "".join(o)

def balanced(src, i):
    """src[i] == '(' ; return index of matching ')'"""
    depth, j, n = 0, i, len(src)
    while j < n:
        c = src[j]
        if c in "\"'":
            k = j + 1
            while k < n and src[k] != c:
                k += 2 if src[k] == "\\" else 1
            j = k
        elif c == "(":
            depth += 1
        elif c == ")":
            depth -= 1
            if depth == 0:
                return j
        j += 1
    return n

LIT = re.compile(r'"((?:[^"\\]|\\.)*)"')
def literals(args):
    """C adjacent-literal concatenation: the first run of adjacent literals."""
    runs, cur, last_end = [], [], None
    for m in LIT.finditer(args):
        if cur and args[last_end:m.start()].strip() == "":
            cur.append(m.group(1))
        else:
            if cur: runs.append("".join(cur))
            cur = [m.group(1)]
        last_end = m.end()
    if cur: runs.append("".join(cur))
    return [bytes(r, "utf-8").decode("unicode_escape") for r in runs]

def func_spans(src):
    """(name, start, end) for top-level C function bodies."""
    spans = []
    for m in re.finditer(r"^([A-Za-z_][A-Za-z0-9_]*)\s*\([^;{]*?\)\s*\{", src, re.M):
        start = m.end() - 1
        depth, j = 0, start
        while j < len(src):
            if src[j] == "{": depth += 1
            elif src[j] == "}":
                depth -= 1
                if depth == 0: break
            j += 1
        spans.append((m.group(1), start, j))
    return spans

def enclosing(spans, pos):
    for name, s, e in spans:
        if s <= pos <= e: return name
    return "?"

CALL = re.compile(r"\b(zfs_error_aux|zfs_error_fmt|zfs_error|zfs_standard_error_fmt|zfs_standard_error|"
                  r"zpool_standard_error_fmt|zpool_standard_error|zfs_setprop_error|"
                  r"fprintf|fputs|libzfs_error_description_case)\s*\(")
EZFS = re.compile(r"\b(EZFS_[A-Z0-9_]+)\b")

entries = []
def add(**kw):
    kw["id"] = f'{kw["file"]}:{kw["line"]}'
    entries.append(kw)

for rel in FILES:
    raw = open(f"{root}/{rel}", encoding="utf-8", errors="replace").read()
    src = strip_comments(raw)
    lineof = lambda p: src.count("\n", 0, p) + 1
    spans = func_spans(src)

    # 1. EZFS descriptions (libzfs_error_description switch)
    if rel.endswith("libzfs_util.c"):
        m = re.search(r"libzfs_error_description\s*\([^)]*\)\s*\{", src)
        body_end = balanced(src, src.find("(", m.end() - 1)) if False else None
        s, depth, j = m.end() - 1, 0, m.end() - 1
        while True:
            if src[j] == "{": depth += 1
            elif src[j] == "}":
                depth -= 1
                if depth == 0: break
            j += 1
        body = src[s:j]
        for cm in re.finditer(r"case\s+(EZFS_[A-Z0-9_]+)\s*:\s*return\s*\(", body):
            p = s + cm.end() - 1
            lits = literals(src[p:balanced(src, p)])
            if lits:
                add(file=rel, line=lineof(p), func="libzfs_error_description", source="desc",
                    ezfs=cm.group(1), template=lits[0])

    # 2. call sites
    calls = []
    for m in CALL.finditer(src):
        p = m.end() - 1
        q = balanced(src, p)
        calls.append((m.start(), m.group(1), src[p+1:q]))
    for idx, (pos, name, args) in enumerate(calls):
        fn = enclosing(spans, pos)
        if name == "zfs_error_aux":
            lits = literals(args)
            if not lits or lits[0] == "%s":
                continue  # dynamic aux (strerror passthrough) is handled as errno corpus
            # pair with the next error-raising call in the same function
            code, via = None, None
            for pos2, name2, args2 in calls[idx+1: idx+12]:
                if enclosing(spans, pos2) != fn: break
                if name2 in ("zfs_error", "zfs_error_fmt"):
                    em = EZFS.search(args2); code = em.group(1) if em else None; via = name2; break
                if name2.startswith(("zfs_standard_error", "zpool_standard_error")):
                    via = name2; break
                if name2 == "zfs_error_aux": break
            add(file=rel, line=lineof(pos), func=fn, source="aux", ezfs=code, via=via, template=lits[0])
        elif name in ("fprintf", "fputs") and rel.startswith("cmd/"):
            if "stderr" not in args: continue
            lits = literals(args)
            if not lits: continue
            t = lits[0]
            if not t.strip() or t.strip() in ("\n", "%s", "%s\n"): continue
            add(file=rel, line=lineof(pos), func=fn, source="cli", ezfs=None, template=t)

# errno -> stderr for zfs_standard_error default branch: strerror() text with EZFS_UNKNOWN
# (process aborts, prints "internal error: ..."). Recorded as one synthetic row.
add(file="lib/libzfs/libzfs_util.c", line=0, func="zfs_standard_error_fmt", source="errno-default",
    ezfs="EZFS_UNKNOWN", template="internal error: Invalid argument")

SPEC = re.compile(r"%[-+ #0]*(\*|\d+)?(\.(\*|\d+))?(hh|h|ll|l|z|j|t|L)?([diouxXeEfgGcsp%])")
def render(t, action="cannot destroy 'tank/k8s/v1'"):
    def sub(m):
        c = m.group(5)
        return "%" if c == "%" else ("tank/k8s/v1" if c == "s" else "1")
    return SPEC.sub(sub, t)

for e in entries:
    body = render(e["template"])
    if e["source"] in ("desc", "aux"):
        # zfs_verror prints "<action>: <description-or-aux>"
        e["stderr"] = "cannot destroy 'tank/k8s/v1': " + body
    else:
        e["stderr"] = body
json.dump(entries, open(out, "w"), indent=1)
by = {}
for e in entries: by[e["source"]] = by.get(e["source"], 0) + 1
print(len(entries), by)
