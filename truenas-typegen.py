#!/usr/bin/env python3
"""
Generate TypeScript declarations for the TrueNAS JSON-RPC methods this driver
uses, from the Pydantic API models in a truenas/middleware checkout.

The models are read with `ast` only; nothing from middleware is imported or
executed. Each API version directory (e.g. v25_10_0, v27_0_0) produces its own
declaration file so callers can be typed against the version the server reports.

usage:
  python3 -I tools/truenas-typegen.py <middleware>/src/middlewared/middlewared/api \
      --out src/types/truenas v25_10_0 v27_0_0
  python3 -I tools/truenas-typegen.py <api dir> --diff v25_10_0 v27_0_0
"""

import argparse
import ast
import json
import os
import re
import sys

# JSON-RPC method -> (kind, model names). kinds:
#   call:   explicit <Prefix>Args / <Prefix>Result models
#   query:  generic query(filters, options) over <Entry>
#   get:    generic get_instance(id, options) over <Entry>
#   config: generic config() over <Entry>
METHODS = {
    "auth.login_with_api_key": ("call", "AuthLoginWithApiKey"),
    "auth.login": ("call", "AuthLogin"),
    "core.get_jobs": ("query", "CoreGetJobsItem"),
    "core.job_wait": ("call", "CoreJobWait"),
    "system.version": ("call", "SystemVersion"),
    "system.info": ("call", "SystemInfo"),
    "service.reload": ("call", "ServiceReload"),
    "filesystem.setperm": ("call", "FilesystemSetperm"),
    "filesystem.chown": ("call", "FilesystemChown"),
    "replication.run_onetime": ("call", "ReplicationRunOnetime"),
    "pool.dataset.query": ("query", "PoolDatasetEntry"),
    "pool.dataset.get_instance": ("get", "PoolDatasetEntry"),
    "pool.dataset.create": ("call", "PoolDatasetCreate"),
    "pool.dataset.update": ("call", "PoolDatasetUpdate"),
    "pool.dataset.delete": ("call", "PoolDatasetDelete"),
    "pool.dataset.destroy_snapshots": ("call", "PoolDatasetDestroySnapshots"),
    "pool.snapshot.query": ("query", "PoolSnapshotEntry"),
    "pool.snapshot.get_instance": ("get", "PoolSnapshotEntry"),
    "pool.snapshot.create": ("call", "PoolSnapshotCreate"),
    "pool.snapshot.update": ("call", "PoolSnapshotUpdate"),
    "pool.snapshot.delete": ("call", "PoolSnapshotDelete"),
    "pool.snapshot.clone": ("call", "PoolSnapshotClone"),
    "iscsi.global.config": ("config", "ISCSIGlobalEntry"),
    "iscsi.extent.query": ("query", "iSCSITargetExtentEntry"),
    "iscsi.extent.create": ("call", "iSCSITargetExtentCreate"),
    "iscsi.extent.update": ("call", "iSCSITargetExtentUpdate"),
    "iscsi.extent.delete": ("call", "iSCSITargetExtentDelete"),
    "iscsi.target.query": ("query", "iSCSITargetEntry"),
    "iscsi.target.create": ("call", "iSCSITargetCreate"),
    "iscsi.target.update": ("call", "iSCSITargetUpdate"),
    "iscsi.target.delete": ("call", "iSCSITargetDelete"),
    "iscsi.targetextent.query": ("query", "iSCSITargetToExtentEntry"),
    "iscsi.targetextent.create": ("call", "iSCSITargetToExtentCreate"),
    "iscsi.targetextent.delete": ("call", "iSCSITargetToExtentDelete"),
    "sharing.nfs.query": ("query", "SharingNFSEntry"),
    "sharing.nfs.create": ("call", "SharingNFSCreate"),
    "sharing.nfs.update": ("call", "SharingNFSUpdate"),
    "sharing.nfs.delete": ("call", "SharingNFSDelete"),
    "sharing.smb.query": ("query", "SharingSMBEntry"),
    "sharing.smb.create": ("call", "SharingSMBCreate"),
    "sharing.smb.update": ("call", "SharingSMBUpdate"),
    "sharing.smb.delete": ("call", "SharingSMBDelete"),
    "nvmet.subsys.query": ("query", "NVMetSubsysEntry"),
    "nvmet.subsys.create": ("call", "NVMetSubsysCreate"),
    "nvmet.subsys.delete": ("call", "NVMetSubsysDelete"),
    "nvmet.port.query": ("query", "NVMetPortEntry"),
    "nvmet.port_subsys.query": ("query", "NVMetPortSubsysEntry"),
    "nvmet.port_subsys.create": ("call", "NVMetPortSubsysCreate"),
    "nvmet.namespace.query": ("query", "NVMetNamespaceEntry"),
    "nvmet.namespace.create": ("call", "NVMetNamespaceCreate"),
    "nvmet.namespace.delete": ("call", "NVMetNamespaceDelete"),
}

PRIMITIVES = {
    "str": "string",
    "int": "number",
    "float": "number",
    "bool": "boolean",
    "None": "null",
    "NoneType": "null",
    "Any": "unknown",
    "object": "unknown",
    "dict": "Record<string, unknown>",
    "Dict": "Record<string, unknown>",
    "list": "unknown[]",
    "List": "unknown[]",
    # datetime is encoded by middleware's JSON-RPC layer, not by Pydantic;
    # left opaque rather than guessing the wire shape
    "datetime": "unknown",
    "date": "unknown",
    "time": "unknown",
}

# middleware string-ish constrained types (api/base/types)
STRINGY = re.compile(
    r"^(NonEmptyString|LongString|LongNonEmptyString|EmailString|HttpUrl|"
    r"UUID|IPvAnyAddress|IPv4Address|IPv6Address|IPvAnyNetwork|IPvAnyInterface|"
    r"TimeString|UnixPerm|.*String|.*Name|.*Path|SNAPSHOT_NAME)$"
)
WRAPPERS = {"Annotated", "Secret", "Required", "NotRequired", "ClassVar", "Final"}
SKIP_FIELDS = {"model_config"}


class Version:
    def __init__(self, api_dir, version):
        self.version = version
        self.classes = {}
        self.aliases = {}
        self.unresolved = set()
        d = os.path.join(api_dir, version)
        if not os.path.isdir(d):
            raise SystemExit(f"no such api version directory: {d}")
        # shared aliases (api/base/types) first, so version modules shadow them
        base = os.path.join(api_dir, "base", "types")
        if os.path.isdir(base):
            for f in sorted(os.listdir(base)):
                if f.endswith(".py"):
                    self._load(os.path.join(base, f))
        for f in sorted(os.listdir(d)):
            if f.endswith(".py"):
                self._load(os.path.join(d, f))

    def _load(self, path):
        tree = ast.parse(open(path, encoding="utf-8").read(), filename=path)
        for node in tree.body:
            if isinstance(node, ast.ClassDef):
                self.classes[node.name] = node
            elif (
                isinstance(node, ast.Assign)
                and len(node.targets) == 1
                and isinstance(node.targets[0], ast.Name)
                and node.targets[0].id != "__all__"
            ):
                self.aliases[node.targets[0].id] = node.value
            elif isinstance(node, ast.AnnAssign) and isinstance(node.target, ast.Name):
                if node.value is not None:
                    self.aliases[node.target.id] = node.value


class Emitter:
    def __init__(self, v):
        self.v = v
        self.interfaces = {}  # name -> list[(field, ts, optional)]
        self.pending = []

    # ---- type expressions -------------------------------------------------

    def ts(self, node, depth=0):
        if depth > 40:
            return "unknown"
        if node is None:
            return "unknown"
        if isinstance(node, ast.Constant):
            if node.value is None:
                return "null"
            if isinstance(node.value, str):
                # forward reference
                try:
                    return self.ts(ast.parse(node.value, mode="eval").body, depth + 1)
                except SyntaxError:
                    return "unknown"
            return json.dumps(node.value)
        if isinstance(node, ast.BinOp) and isinstance(node.op, ast.BitOr):
            return self.union([self.ts(node.left, depth + 1), self.ts(node.right, depth + 1)])
        if isinstance(node, ast.Attribute):
            return self.name(node.attr, depth)
        if isinstance(node, ast.Name):
            return self.name(node.id, depth)
        if isinstance(node, ast.Subscript):
            base = node.value.attr if isinstance(node.value, ast.Attribute) else getattr(node.value, "id", "")
            args = node.slice.elts if isinstance(node.slice, ast.Tuple) else [node.slice]
            if base in WRAPPERS:
                return self.ts(args[0], depth + 1)
            if base == "Literal":
                return self.union([json.dumps(a.value) for a in args if isinstance(a, ast.Constant)])
            if base in ("Union",):
                return self.union([self.ts(a, depth + 1) for a in args])
            if base == "Optional":
                return self.union([self.ts(args[0], depth + 1), "null"])
            if base in ("list", "List", "set", "Set", "frozenset", "Sequence", "UniqueList"):
                return self.array(self.ts(args[0], depth + 1))
            if base in ("tuple", "Tuple"):
                return self.array(self.union([self.ts(a, depth + 1) for a in args if not isinstance(a, ast.Constant)]) or "unknown")
            if base in ("dict", "Dict", "Mapping"):
                return f"Record<string, {self.ts(args[1], depth + 1) if len(args) > 1 else 'unknown'}>"
            if base == "Excluded":
                return "never"
            return self.name(base, depth)
        if isinstance(node, ast.Call):
            # e.g. excluded_field(), conlist(...)
            fn = getattr(node.func, "id", getattr(node.func, "attr", ""))
            if fn == "excluded_field":
                return "never"
            return "unknown"
        return "unknown"

    def name(self, n, depth):
        if n in PRIMITIVES:
            return PRIMITIVES[n]
        if n == "Excluded":
            return "never"
        if n in self.v.classes:
            cls = self.v.classes[n]
            if self.is_enum(cls):
                return self.enum(cls)
            self.require(n)
            return n
        if n in self.v.aliases:
            return self.ts(self.v.aliases[n], depth + 1)
        if STRINGY.match(n):
            return "string"
        self.v.unresolved.add(n)
        return "unknown"

    def is_enum(self, cls):
        return any(getattr(b, "id", getattr(b, "attr", "")) in ("Enum", "StrEnum", "IntEnum") for b in cls.bases)

    def enum(self, cls):
        vals = [
            json.dumps(s.value.value)
            for s in cls.body
            if isinstance(s, ast.Assign) and isinstance(s.value, ast.Constant)
        ]
        return self.union(vals) or "unknown"

    @staticmethod
    def union(parts):
        out = []
        for p in parts:
            for q in p.split(" | ") if not p.startswith("Record<") and "(" not in p else [p]:
                if q and q not in out and q != "never":
                    out.append(q)
        if "unknown" in out:
            return "unknown"
        return " | ".join(out)

    @staticmethod
    def array(t):
        return f"({t})[]" if " | " in t else f"{t}[]"

    # ---- models -------------------------------------------------------------

    def require(self, n):
        if n not in self.interfaces and n not in self.pending:
            self.pending.append(n)

    def fields(self, n, seen=None):
        """resolved [(name, ts, optional)] including inherited, honoring Excluded"""
        seen = seen or set()
        if n in seen or n not in self.v.classes:
            return []
        seen.add(n)
        cls = self.v.classes[n]
        result = {}
        for b in cls.bases:
            bn = getattr(b, "id", getattr(b, "attr", None))
            if bn and bn in self.v.classes:
                for f in self.fields(bn, seen):
                    result[f[0]] = f
        for_update = any(
            k.arg == "metaclass" and getattr(k.value, "id", "") == "ForUpdateMetaclass" for k in cls.keywords
        )
        for s in cls.body:
            if not isinstance(s, ast.AnnAssign) or not isinstance(s.target, ast.Name):
                continue
            fname = s.target.id
            if fname in SKIP_FIELDS:
                continue
            ann = ast.unparse(s.annotation)
            if ann.startswith("ClassVar"):
                continue
            t = self.ts(s.annotation)
            if t == "never" or (s.value is not None and "excluded_field" in ast.unparse(s.value)):
                result.pop(fname, None)
                result.pop(self.alias_of(s) or fname, None)
                continue
            optional = s.value is not None and not self.default_is_required(s.value)
            name = self.alias_of(s) or fname
            if fname != name:
                result.pop(fname, None)
            result[name] = (name, t, optional)
        if self.allows_extra(cls):
            result["[key: string]"] = ("[key: string]", "unknown", False)
        if for_update:
            return [(k, t, not k.startswith("[")) for k, (_, t, _o) in result.items()]
        return list(result.values())

    @staticmethod
    def allows_extra(cls):
        for s in cls.body:
            if (
                isinstance(s, ast.Assign)
                and getattr(s.targets[0], "id", "") == "model_config"
                and re.search(r"extra\s*=\s*['\"]allow['\"]", ast.unparse(s.value))
            ):
                return True
        return False

    @staticmethod
    def alias_of(s):
        if isinstance(s.value, ast.Call):
            for k in s.value.keywords:
                if k.arg in ("alias", "validation_alias") and isinstance(k.value, ast.Constant):
                    return k.value.value
        return None

    @staticmethod
    def default_is_required(v):
        # Field(description=...) with no default / default_factory means required
        if isinstance(v, ast.Call) and getattr(v.func, "id", "") == "Field":
            has_default = any(k.arg in ("default", "default_factory") for k in v.keywords) or (
                v.args and not (isinstance(v.args[0], ast.Constant) and v.args[0].value is Ellipsis)
            )
            return not has_default
        return False

    def decorator_arg(self, cls, name):
        for d in cls.decorator_list:
            if isinstance(d, ast.Call) and getattr(d.func, "id", "") == name and d.args:
                return d.args[0].value
        return None

    def drain(self):
        while self.pending:
            n = self.pending.pop(0)
            if n in self.interfaces:
                continue
            self.interfaces[n] = None  # guard recursion
            self.interfaces[n] = self.fields(n)

    # ---- methods ------------------------------------------------------------

    def method(self, method, kind, model):
        v = self.v
        if kind in ("query", "get", "config"):
            if model not in v.classes:
                raise KeyError(model)
            self.require(model)
            if kind == "query":
                return ["filters?: QueryFilter[]", "options?: QueryOptions"], f"{model}[] | {model} | number"
            if kind == "get":
                return ["id: string | number", "options?: QueryOptions"], model
            return [], model
        args, res = model + "Args", model + "Result"
        if args not in v.classes or res not in v.classes:
            raise KeyError(args if args not in v.classes else res)
        acls = v.classes[args]
        single = self.decorator_arg(acls, "single_argument_args")
        if single:
            self.require(args)
            params = [f"{single}: {args}"]
        else:
            params = [f"{n}{'?' if o else ''}: {t}" for n, t, o in self.fields(args)]
            # a required positional after an optional one is invalid TS; keep order, loosen
            seen_opt = False
            fixed = []
            for p in params:
                n, t = p.split(": ", 1)
                if n.endswith("?"):
                    seen_opt = True
                elif seen_opt:
                    n += "?"
                fixed.append(f"{n}: {t}")
            params = fixed
        rfields = {n: t for n, t, _ in self.fields(res)}
        rtype = rfields.get("result", "unknown")
        if any(isinstance(d, ast.Name) and d.id == "single_argument_result" for d in v.classes[res].decorator_list):
            rtype = res
            self.require(res)
        return params, rtype


def generate(api_dir, version, strict):
    v = Version(api_dir, version)
    e = Emitter(v)
    methods, missing = {}, {}
    for m, (kind, model) in METHODS.items():
        try:
            methods[m] = e.method(m, kind, model)
        except KeyError as err:
            missing[m] = str(err)
    e.drain()
    if missing and strict:
        raise SystemExit(f"{version}: missing models: {json.dumps(missing, indent=2)}")
    return v, e, methods, missing


def render(version, e, methods, missing):
    dotted = version.lstrip("v").replace("_", ".")
    out = [
        "// GENERATED by tools/truenas-typegen.py from truenas/middleware",
        f"// src/middlewared/middlewared/api/{version} -- do not edit by hand.",
        "",
        "export type QueryFilter = [string, string, unknown] | [\"OR\", QueryFilter[]] | unknown[];",
        "export interface QueryOptions {",
        "  extra?: Record<string, unknown>;",
        "  order_by?: string[];",
        "  select?: (string | unknown[])[];",
        "  count?: boolean;",
        "  get?: boolean;",
        "  offset?: number;",
        "  limit?: number;",
        "  force_sql_filters?: boolean;",
        "}",
        "",
        f'export type ApiVersion = "v{dotted}";',
        "",
        "export interface Methods {",
    ]
    for m in sorted(methods):
        params, result = methods[m]
        out.append(f'  "{m}": {{ params: [{", ".join(params)}]; result: {result} }};')
    out.append("}")
    if missing:
        out.append("")
        out.append("// methods with no models in this version:")
        for m, why in sorted(missing.items()):
            out.append(f"//   {m}: {why}")
    for name in sorted(e.interfaces):
        out.append("")
        out.append(f"export interface {name} {{")
        for fname, t, opt in e.interfaces[name] or []:
            if fname.startswith("["):
                out.append(f"  {fname}: unknown;")
                continue
            key = fname if re.match(r"^[A-Za-z_$][\w$]*$", fname) else json.dumps(fname)
            out.append(f"  {key}{'?' if opt else ''}: {t};")
        out.append("}")
    return "\n".join(out) + "\n"


def flatten(e, methods):
    """method -> {path: (type, optional)}, one level into named models; for --diff"""
    flat = {}
    for m, (params, result) in methods.items():
        d = {}
        for p in params:
            n, t = p.split(": ", 1)
            d["param " + n.rstrip("?")] = (t, n.endswith("?"))
            for model in re.findall(r"\b[A-Za-z]\w*\b", t):
                for fn, ft, fo in e.interfaces.get(model) or []:
                    d[f"param {n.rstrip('?')}.{fn}"] = (ft, fo)
        d["result"] = (result, False)
        for model in re.findall(r"\b[A-Za-z]\w*\b", result):
            for fn, ft, fo in e.interfaces.get(model) or []:
                d[f"result.{fn}"] = (ft, fo)
        flat[m] = d
    return flat


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("api_dir")
    ap.add_argument("versions", nargs="+")
    ap.add_argument("--out")
    ap.add_argument("--diff", action="store_true")
    ap.add_argument("--strict", action="store_true")
    a = ap.parse_args()

    gens = {ver: generate(a.api_dir, ver, a.strict) for ver in a.versions}

    if a.diff:
        (va, (_, ea, ma, xa)), (vb, (_, eb, mb, xb)) = list(gens.items())[:2]
        fa, fb = flatten(ea, ma), flatten(eb, mb)
        for m in sorted(set(fa) | set(fb)):
            if m not in fb:
                print(f"{m}: REMOVED in {vb}")
                continue
            if m not in fa:
                print(f"{m}: ADDED in {vb}")
                continue
            lines = []
            for k in sorted(set(fa[m]) | set(fb[m])):
                A, B = fa[m].get(k), fb[m].get(k)
                if A == B:
                    continue
                if A is None:
                    lines.append(f"  + {k}{'?' if B[1] else ''}: {B[0]}")
                elif B is None:
                    lines.append(f"  - {k}: {A[0]}")
                else:
                    lines.append(f"  ~ {k}: {A[0]}{'?' if A[1] else ''}  ->  {B[0]}{'?' if B[1] else ''}")
            if lines:
                print(f"{m}:")
                print("\n".join(lines))
        return

    # runtime table: positional parameter names per method, used to map
    # REST-style object bodies onto JSON-RPC positional params
    param_names = {}
    for ver, (v, e, methods, missing) in gens.items():
        if v.unresolved:
            print(f"{ver}: typed as unknown: {', '.join(sorted(v.unresolved))}", file=sys.stderr)
        if missing:
            print(f"{ver}: missing: {', '.join(sorted(missing))}", file=sys.stderr)
        print(f"{ver}: {len(methods)} methods, {len(e.interfaces)} models", file=sys.stderr)
        dotted = "v" + ver.lstrip("v").replace("_", ".")
        param_names[dotted] = {
            m: [p.split(": ", 1)[0].rstrip("?") for p in params] for m, (params, _) in sorted(methods.items())
        }
        if a.out:
            os.makedirs(a.out, exist_ok=True)
            with open(os.path.join(a.out, f"{ver}.d.ts"), "w") as fh:
                fh.write(render(ver, e, methods, missing))

    if a.out:
        with open(os.path.join(a.out, "methods.json"), "w") as fh:
            json.dump(param_names, fh, indent=2, sort_keys=True)
            fh.write("\n")
        vers = list(gens)
        index = ["// GENERATED by tools/truenas-typegen.py -- do not edit by hand.", ""]
        for ver in vers:
            index.append(f'import type * as {ver} from "./{ver}";')
        index.append("")
        index.append("export interface MethodsByVersion {")
        for ver in vers:
            index.append(f'  "v{ver.lstrip("v").replace("_", ".")}": {ver}.Methods;')
        index.append("}")
        index.append("export type ApiVersion = keyof MethodsByVersion;")
        index.append("/** methods present in every generated version, typed by the newest */")
        index.append(f"export type Methods = {vers[-1]}.Methods;")
        index.append("export type MethodName = keyof Methods;")
        index.append('export type Params<M extends MethodName> = Methods[M]["params"];')
        index.append('export type Result<M extends MethodName> = Methods[M]["result"];')
        index.append(f"export type {{ QueryFilter, QueryOptions }} from \"./{vers[-1]}\";")
        with open(os.path.join(a.out, "index.d.ts"), "w") as fh:
            fh.write("\n".join(index) + "\n")


if __name__ == "__main__":
    main()
