import hashlib, struct

seed = 7160208141875502625
target = "8bcdfff3f1cac450"
seedb = struct.pack("<Q", seed)

def encodings(data):
    # Go fuzz corpus encoding: "go test fuzz v1\n[]byte(%q)\n"
    s = "".join(
        chr(c) if 0x20 <= c < 0x7f and chr(c) not in '"\\' else
        ('\\"' if chr(c) == '"' else '\\\\' if chr(c) == '\\' else '\\x%02x' % c)
        for c in data)
    yield ("go test fuzz v1\n[]byte(\"" + s + "\")\n").encode()

hits = []
for x in range(256):
    if x % 40 != 27:
        continue
    data = seedb + bytes([x])
    for enc in encodings(data):
        for name, h in (("sha256", hashlib.sha256(enc).hexdigest()),):
            if h.startswith(target):
                hits.append((x, name, enc))
            print(x, h[:16], "match" if h.startswith(target) else "")
print("hits:", hits)
