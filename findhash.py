import hashlib, itertools, sys

TARGET = "8dbaba9de1f231eb"

def gq(b: bytes) -> str:
    # approximates Go strconv.Quote for ASCII-range bytes
    out = ['"']
    for c in b:
        ch = chr(c)
        if ch == '"':
            out.append('\\"')
        elif ch == '\\':
            out.append('\\\\')
        elif c == 0x07:
            out.append('\\a')
        elif c == 0x08:
            out.append('\\b')
        elif c == 0x0c:
            out.append('\\f')
        elif c == 0x0a:
            out.append('\\n')
        elif c == 0x0d:
            out.append('\\r')
        elif c == 0x09:
            out.append('\\t')
        elif c == 0x0b:
            out.append('\\v')
        elif c < 0x20 or c == 0x7f or c >= 0x80:
            out.append('\\x%02x' % c)
        else:
            out.append(ch)
    out.append('"')
    return ''.join(out)

def contents(etab: str, data: bytes) -> bytes:
    return ("go test fuzz v1\nstring(%s)\n[]byte(%s)\n" % (gq(etab.encode()), gq(data))).encode()

etabs = []
base = [
    "0\t(0)\n/tank/v/a\t10.0.0.0/24(ro)\n",
    "0\t(0)\n/tank/v/a\t10.0.0.0/24(ro)",
    "0\t(0)\n/tank/v/a\t10.0.0.0/24(ro)\n\n",
    "0\t(0)\n/tank/v/a\t10.0.0.0/24(ro)\n",
]
etabs += base
datas = [b"", b"0", b"\x00", b"\x01", b"\x01\x00\x00\x00\x04\x01", b"\x02\x03\x01\x00\x01\x02\x00\x00\x03\x01\x01\x00\x02",
         b"\x01\x00\x00\x00\x01\x00\x03\x00\x04\x01\x00\x00\x00\x00\x00", b"1", b"00", b"\x00\x00", b"\x01\x00", b"a", b"\xff"]
for n in range(1, 3):
    for t in itertools.product(range(0, 8), repeat=n):
        datas.append(bytes(t))
for n in range(1, 2):
    for t in itertools.product(range(256), repeat=n):
        datas.append(bytes(t))

seen = 0
for e in etabs:
    for d in datas:
        seen += 1
        h = hashlib.sha256(contents(e, d)).hexdigest()[:16]
        if h == TARGET:
            print("MATCH", repr(e), repr(d))
            sys.stdout.write(contents(e, d).decode())
            sys.exit(0)
print("no match among", seen)
# sanity: the known reproducers
for name, e, d in [("3019a92d2157f624", "/tank/\"\"v/a\t()", b"0"), ("5d64c912336f95f8", "/tank/v/a\v\t()", b"0")]:
    print(name, hashlib.sha256(contents(e, d)).hexdigest()[:16])
