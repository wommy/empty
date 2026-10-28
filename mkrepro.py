import struct, sys

data = struct.pack("<Q", 7160208141875502625) + bytes([67])
s = "".join(
    chr(c) if 0x20 <= c < 0x7f and chr(c) not in '"\\' else
    ('\\"' if chr(c) == '"' else '\\\\' if chr(c) == '\\' else '\\x%02x' % c)
    for c in data)
out = "go test fuzz v1\n[]byte(\"" + s + "\")\n"
dst = sys.argv[1]
with open(dst, "wb") as f:
    f.write(out.encode())
print(out.encode())
