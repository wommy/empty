import re, os, sys
ROOT = sys.argv[1]
FILES = ["src/driver/freenas/api.js","src/driver/freenas/ssh.js","src/driver/freenas/http/api.js","src/driver/freenas/http/index.js","src/driver/freenas/index.js","src/driver/controller-synology/index.js","src/driver/controller-synology/http/index.js"]
# control-flow version branches: conditionals on version/scale/apiVersion
cats = {
 "switch(apiVersion)": r"switch\s*\(\s*apiVersion\s*\)",
 "if apiVersion==N": r"apiVersion\s*==\s*\d",
 "semver.satisfies": r"semver\.satisfies\(",
 "isScale check": r"isScale\b",
 "getIsScale call": r"getIsScale\(",
 "getSystemVersion*": r"getSystemVersion(Semver|Major|MajorMinor)?\(",
 "csiVersion semver": r"semver\.satisfies\(\s*(this\.)?(driver\.)?ctx\.csiVersion",
 "websocket/jsonrpc": r"(?i)websocket|jsonrpc|json-rpc|ws://|wss://",
}
for f in FILES:
    p = os.path.join(ROOT, f)
    if not os.path.exists(p):
        print(f, "MISSING"); continue
    src = open(p, encoding="utf-8").read().splitlines()
    counts = {k: 0 for k in cats}
    for line in src:
        for k, rx in cats.items():
            if re.search(rx, line):
                counts[k] += 1
    total = sum(1 for l in src if l.strip())
    print(f"== {f} ({len(src)} lines)")
    for k, v in counts.items():
        print(f"   {k:22s} {v}")
