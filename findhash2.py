import hashlib, itertools, sys

TARGET = "8dbaba9de1f231eb"

def gq(b: bytes) -> str:
    out = ['"']
    for c in b:
        ch = chr(c)
        if ch == '"':
            out.append('\\"')
        elif ch == '\\':
            out.append('\\\\')
        elif c == 0x07: out.append('\\a')
        elif c == 0x08: out.append('\\b')
        elif c == 0x0c: out.append('\\f')
        elif c == 0x0a: out.append('\\n')
        elif c == 0x0d: out.append('\\r')
        elif c == 0x09: out.append('\\t')
        elif c == 0x0b: out.append('\\v')
        elif c < 0x20 or c == 0x7f or c >= 0x80:
            out.append('\\x%02x' % c)
        else:
            out.append(ch)
    out.append('"')
    return ''.join(out)

def contents(etab: str, data: bytes) -> bytes:
    return ("go test fuzz v1\nstring(%s)\n[]byte(%s)\n" % (gq(etab.encode()), gq(data))).encode()

etabs = ["0\t(0)\n/tank/v/a\t10.0.0.0/24(ro)\n", "0\t(0)\n/tank/v/a\t10.0.0.0/24(ro)"]

def gen():
    for n in range(0, 3):
        for t in itertools.product(range(256), repeat=n):
            yield bytes(t)
    for n in (3, 4):
        for t in itertools.product(range(8), repeat=n):
            yield bytes(t)

cnt = 0
for e in etabs:
    for d in gen():
        cnt += 1
        if hashlib.sha256(contents(e, d)).hexdigest()[:16] == TARGET:
            print("MATCH", repr(e), repr(d))
            print(contents(e, d).decode(), end="")
            sys.exit(0)
print("no match", cnt)
