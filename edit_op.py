def edit(p, pairs):
    s = open(p).read()
    for a, b in pairs:
        assert s.count(a) == 1, (p, a)
        s = s.replace(a, b)
    open(p, 'w').write(s)
edit('internal/endpoint/plan.go', [
("""	OpSet
	OpDestroy
)""", """	OpSet
	OpDestroy
	// OpMount: `zfs mount` of a filesystem; already mounted is done
	// (OkExists).
	OpMount
)"""),
("""	OpClone: "clone", OpSet: "set", OpDestroy: "destroy",
}""", """	OpClone: "clone", OpSet: "set", OpDestroy: "destroy", OpMount: "mount",
}"""),
])
edit('internal/endpoint/apply.go', [
("""		case OpDestroy:
			err = e.zfs.Destroy(ctx, op.Target, zfs.DestroyOptions{Recursive: op.Recursive, Defer: op.Defer})
""", """		case OpDestroy:
			err = e.zfs.Destroy(ctx, op.Target, zfs.DestroyOptions{Recursive: op.Recursive, Defer: op.Defer})
		case OpMount:
			err = e.zfs.Mount(ctx, op.Target)
"""),
])
edit('internal/endpoint/apply_test.go', [
("""		OpClone: "clone", OpSet: "set", OpDestroy: "destroy", 0: "OpKind(0)", 99: "OpKind(99)",""",
 """		OpClone: "clone", OpSet: "set", OpDestroy: "destroy", OpMount: "mount", 0: "OpKind(0)", 99: "OpKind(99)","""),
])
