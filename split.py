#!/usr/bin/env python3
"""split.py <repo> <commit-no>: print hunks for commit n as a patch on stdout
(from `git diff -U3` of the working tree vs HEAD-of-branch-start), or with
'seams' write the staged seams_test.go content for commit n to stdout."""
import re, subprocess, sys

repo, what, n = sys.argv[1], sys.argv[2], int(sys.argv[3])
BASE = "088e69b"

HUNKS = {  # file -> {old-start: commit}
    "internal/endpoint/endpoint.go": {123: 4, 195: 4, 295: 1, 423: 4, 553: 3, 588: 3, 642: 3},
    "internal/endpoint/plan.go": {62: 4, 140: 4, 164: 4, 186: 2, 203: 4, 321: 4},
    "internal/endpoint/reach_helpers_test.go": {13: 1, 131: 1},
    "internal/endpoint/endpoint_test.go": {366: 3},
    "internal/endpoint/reconcile.go": {466: 3, 533: 3},
    "internal/endpoint/reconcile_contract_test.go": {165: 3},
    "internal/server/server_test.go": {136: 3},
}

SEAMS = {
    "TestCreateNeverStampsRowWithoutName": 2, "TestPlanCreateDoesNotAdoptUnnamedManaged": 2,
    "TestPageResumesAfterDeletedToken": 3, "TestListTokenSurvivesDeletedVolume": 3,
    "TestReconcileSkipsNonIssuableDesiredID": 3,
    "TestPlanCloneRoundsToSourceBlockSize": 4, "TestCloneOfLargeBlockZvolProvisions": 4,
    "TestCreateResumeFailureStaysAborted": 4, "TestPlanCreateCommitsOnlyMounted": 4,
    "unmountAfterCreate": 4, "TestCreateUnmountedFilesystemIsUnavailable": 4,
}


def patch():
    out = []
    for f, hunks in HUNKS.items():
        d = subprocess.run(["git", "-C", repo, "diff", "-U3", BASE, "--", f], capture_output=True, text=True, check=True).stdout
        parts = re.split(r"(?m)^(?=@@ )", d)
        head, body = parts[0], parts[1:]
        sel = [h for h in body if hunks[int(re.match(r"@@ -(\d+)", h).group(1))] == n]
        if sel:
            out.append(head + "".join(sel))
    sys.stdout.write("".join(out))


def seams():
    s = open(repo + "/internal/endpoint/seams_test.go").read()
    imp_end = s.index(")\n", s.index("import (")) + 2
    header_comment = s[: s.index("import (")]
    rest = s[imp_end:]
    # top-level decls: a comment block followed by func/type, split at blank line + "//" or "func"/"type"
    chunks = re.split(r"\n\n(?=// |func |type )", rest.strip("\n"))
    keep = []
    for c in chunks:
        m = re.search(r"(?m)^(?:func (?:\([^)]*\) )?|type )(\w+)", c)
        name = m.group(1)
        if name == "Run":
            name = "unmountAfterCreate"
        if SEAMS[name] <= n:
            keep.append(c)
    body = "\n\n".join(keep)
    imps = []
    for pkg, path in [("context", '"context"'), ("slices", '"slices"'), ("strings", '"strings"'), ("testing", '"testing"')]:
        if pkg + "." in body:
            imps.append("\t" + path)
    third = []
    if "zfs." in body:
        third.append('\t"github.com/wommy/empty/internal/zfs"')
    if "zfstest." in body:
        third.append('\t"github.com/wommy/empty/internal/zfs/zfstest"')
    imp = "import (\n" + "\n".join(imps) + ("\n\n" + "\n".join(third) if third else "") + "\n)\n"
    sys.stdout.write(header_comment + imp + "\n" + body + "\n")


patch() if what == "patch" else seams()
