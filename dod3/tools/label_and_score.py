#!/usr/bin/env python3
"""Label each corpus string with the INTENDED Kind (from libzfs's own EZFS_*
code, overridden by aux text where the EZFS code is coarse), scope it to the
zfs subcommands the endpoint runs, join with what the code actually does
(classified.json from TestCorpusDump), and score.

usage: label_and_score.py corpus.json classified.json out-labeled.json
"""
import json, re, sys, collections

corpus = json.load(open(sys.argv[1]))
got = {r["id"]: r for r in json.load(open(sys.argv[2]))}

# intended Kind -> intended endpoint code (and why). New kinds the current
# classifier cannot produce are marked NEW.
CODE = {"NotFound": 5, "Exists": 6, "ParentNotFound": 14, "HasChildren": 9, "HasClones": 9,
        "Busy": 9, "Held": 9, "NoSpace": 8, "InvalidProperty": 3, "Permission": 9,
        "Unavailable": 14, "ReadOnly": 14, "KeyUnavailable": 14, "Unsupported": 9,
        "Internal": 13}
NEW = {"Held", "ReadOnly", "KeyUnavailable", "Unsupported"}

EZFS = {
 "EZFS_NOMEM": "Internal", "EZFS_BADPROP": "InvalidProperty", "EZFS_PROPREADONLY": "InvalidProperty",
 "EZFS_PROPTYPE": "InvalidProperty", "EZFS_PROPNONINHERIT": "InvalidProperty",
 "EZFS_PROPSPACE": "InvalidProperty", "EZFS_BADTYPE": "Internal", "EZFS_BUSY": "Busy",
 "EZFS_EXISTS": "Exists", "EZFS_NOENT": "NotFound", "EZFS_DSREADONLY": "ReadOnly",
 "EZFS_VOLTOOBIG": "InvalidProperty", "EZFS_INVALIDNAME": "InvalidProperty",
 "EZFS_BADVERSION": "Unsupported", "EZFS_POOLUNAVAIL": "Unavailable", "EZFS_CROSSTARGET": "Internal",
 "EZFS_ZONED": "Permission", "EZFS_MOUNTFAILED": "Unavailable", "EZFS_UMOUNTFAILED": "Busy",
 "EZFS_UNSHARENFSFAILED": "Busy", "EZFS_SHARENFSFAILED": "Unavailable", "EZFS_PERM": "Permission",
 "EZFS_NOSPC": "NoSpace", "EZFS_FAULT": "Internal", "EZFS_IO": "Unavailable", "EZFS_INTR": "Unavailable",
 "EZFS_RECURSIVE": "Internal", "EZFS_NAMETOOLONG": "InvalidProperty", "EZFS_NODELEGATION": "Permission",
 "EZFS_UNSHARESMBFAILED": "Busy", "EZFS_SHARESMBFAILED": "Unavailable", "EZFS_NOTSUP": "Unsupported",
 "EZFS_POOLREADONLY": "ReadOnly", "EZFS_ACTIVE_POOL": "Unavailable", "EZFS_CRYPTOFAILED": "KeyUnavailable",
 "EZFS_IOC_NOTSUPPORTED": "Unavailable", "EZFS_TOOMANY": "Internal", "EZFS_WRONG_PARENT": "Unsupported",
 "EZFS_CKSUM": "Unavailable", "EZFS_UNKNOWN": "Internal", "EZFS_SHAREFAILED": "Unavailable",
 "EZFS_NO_USER_NS_SUPPORT": "Unsupported", "EZFS_NOT_USER_NAMESPACE": "Unsupported",
 "EZFS_POOL_NOTSUP": "Unsupported", "EZFS_POOL_INVALARG": "Internal",
}
# aux text refines a coarse EZFS code (first match wins)
TEXT = [
 (r"snapshot is cloned|has dependent clones", "HasClones"), (r"has children", "HasChildren"),
 (r"parent does not exist|no such parent", "ParentNotFound"),
 (r"size is greater than available space", "NoSpace"),
 (r"it's being held", "Held"), (r"key is not loaded", "KeyUnavailable"),
 (r"pool i/o is currently suspended", "Unavailable"), (r"module does not support", "Unavailable"),
 (r"no such pool(?! or dataset)", "Unavailable"),
 (r"feature not enabled|must be upgraded", "Unsupported"),
 (r"multiple snapshots of same fs", "Internal"), (r"failed to lookup parent", "Internal"),
 (r"dataset does not exist|could not find any snapshots", "NotFound"),
 (r"dataset already exists", "Exists"), (r"dataset is busy", "Busy"),
]
# aux strings raised by a caller (no EZFS code at the call site): by function
FUNC = {
 "zfs_validate_name": "InvalidProperty", "zfs_nicestrtonum": "InvalidProperty",
 "zprop_parse_value": "InvalidProperty", "zfs_valid_proplist": "InvalidProperty",
 "zfs_mount_at": "Unavailable", "zfs_crypto_create": "InvalidProperty",
 "zfs_crypto_clone_check": "InvalidProperty", "populate_create_encryption_params_nvlists": "InvalidProperty",
}
CLI = {1432: "HasChildren", 1453: "HasClones", 1779: "NotFound", 2537: "InvalidProperty",
       2543: "InvalidProperty", 2559: "InvalidProperty", 2564: "InvalidProperty",
       3925: "InvalidProperty", 3933: "InvalidProperty"}

# scope: what `zfs create|destroy|snapshot|clone|set|get|list|inherit|promote` can print
SCOPE_FILES = {"lib/libzfs/libzfs_dataset.c", "lib/libzfs/libzfs_util.c", "lib/libzfs/libzfs_mount.c",
               "lib/libzfs/libzfs_changelist.c", "lib/libzfs/libzfs_iter.c",
               "lib/libzfs/os/linux/libzfs_mount_os.c", "lib/libzfs/os/linux/libzfs_util_os.c"}
OUT_FUNC = re.compile(r"rename|hold|release|userspace|bookmark|diff|send|recv|redact|perm|allow|wait|"
                      r"program|jail|zone|smb|unshare|share_|zpool_|vdev|import|history|resume|"
                      r"get_key|load_key|unload_key|rewrap|keylocation|channel|project|sync_|"
                      r"rollback|fsacl|zfs_iter_snapspec|zfs_spa_version|groupquota|userquota|written")
CLI_FUNC = re.compile(r"zfs_do_(create|destroy|snapshot|clone|set|get|list|inherit|promote)\b|"
                      r"destroy_check_dependent|parseprop|parsepropname|parse_depth|get_callback|nomem|^main$")
CLI_OUT_LINES = {1826, 1832, 1838, 1851, 2098, 4046}  # bookmark-only or exit-0 notices

def intended(e):
    t = e["template"].lower()
    if e["source"] == "cli":
        return CLI.get(e["line"], "Internal")  # usage errors: args are ours, so a bug
    for rx, k in TEXT:
        if re.search(rx, t):
            return k
    if e.get("ezfs") in EZFS:
        return EZFS[e["ezfs"]]
    if e["func"] in FUNC:
        return FUNC[e["func"]]
    if e.get("ezfs"):
        return "OutOfScope"   # pool/vdev/scrub/send/... codes
    return "Unlabeled"

def in_scope(e, k):
    if k in ("OutOfScope", "Unlabeled"):
        return False
    if e["source"] == "desc" or e["source"] == "errno-default":
        return True
    if e["source"] == "cli":
        return bool(CLI_FUNC.search(e["func"])) and e["line"] not in CLI_OUT_LINES
    if e["file"] == "lib/libzfs/libzfs_crypto.c":
        return e["func"] in ("zfs_crypto_create", "zfs_crypto_clone_check")
    return e["file"] in SCOPE_FILES and not OUT_FUNC.search(e["func"])

KIND_CODE = {"unknown": 13}
rows = []
for e in corpus:
    k = intended(e)
    g = got[e["id"]]
    want = CODE.get(k)
    r = dict(id=e["id"], func=e["func"], source=e["source"], ezfs=e.get("ezfs"),
             template=e["template"].strip(), intended_kind=k, intended_code=want,
             actual_kind=g["kind"], actual_code=g["code"], in_scope=in_scope(e, k))
    if want is not None:
        if g["code"] == want:
            r["verdict"] = "correct"
        elif g["kind"] != "unknown" and g["code"] == 13:
            r["verdict"] = "typeI-classified-unmapped"  # Kind known, zfsErr drops it to Internal
        elif g["kind"] == "unknown":
            r["verdict"] = "typeII-unclassified"   # should have been decisive; fell to Internal
        else:
            r["verdict"] = "typeI-misclassified"   # decisive, but the wrong decision
    rows.append(r)
json.dump(rows, open(sys.argv[3], "w"), indent=1)

def pct(a, b): return f"{a}/{b} = {100*a/b:.1f}%" if b else "n/a"
allr = rows
sc = [r for r in rows if r["in_scope"]]
decisive = [r for r in sc if r["intended_kind"] != "Internal"]
print("ALL extracted strings:", len(allr))
print("  classified (Kind != unknown):", pct(sum(r["actual_kind"] != "unknown" for r in allr), len(allr)))
print("IN-SCOPE (reachable from the 9 zfs subcommands used):", len(sc))
print("  classified (Kind != unknown):", pct(sum(r["actual_kind"] != "unknown" for r in sc), len(sc)))
print("  correct endpoint code:", pct(sum(r.get("verdict") == "correct" for r in sc), len(sc)))
print("IN-SCOPE DECISIVE (intended Kind != Internal):", len(decisive))
print("  classified to intended code:", pct(sum(r.get("verdict") == "correct" for r in decisive), len(decisive)))
print("  type II (unclassified -> Internal):", sum(r.get("verdict") == "typeII-unclassified" for r in decisive))
print("  type I (wrong decisive Kind):", sum(r.get("verdict") == "typeI-misclassified" for r in sc))
print("  classified but unmapped (Kind known, code Internal):", sum(r.get("verdict") == "typeI-classified-unmapped" for r in sc))
print("  of which need a NEW Kind:", sum(r["intended_kind"] in NEW for r in decisive))
print("by intended kind (in-scope decisive): correct/total")
c = collections.defaultdict(lambda: [0, 0])
for r in decisive:
    c[r["intended_kind"]][1] += 1
    c[r["intended_kind"]][0] += r.get("verdict") == "correct"
for k, (a, b) in sorted(c.items(), key=lambda x: -x[1][1]):
    print(f"  {k:16} {a}/{b}")
print("unlabeled (excluded):", sum(r["intended_kind"] == "Unlabeled" for r in rows),
      " out-of-scope:", sum(r["intended_kind"] == "OutOfScope" or not r["in_scope"] for r in rows))
