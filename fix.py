import sys
p=sys.argv[1]
s=open(p).read()
s=s.replace('''				plan := Plan{{Kind: OpSet, Target: "tank/a"}, {Kind: c.op, Target: "tank/b", Source: "tank/a@s"}}
				i := 0
				if c.mutated {
					i = 1
				}
				got, want = opErr(err, i, plan), headApplyErr(err, i, plan)
				// a resumed provision: an earlier attempt changed the pool
				g, w := resumeErr(opErr(err, 0, plan)), headResumeErr(headApplyErr(err, 0, plan))''','''				plan, i := planAt(c)
				got, want = opErr(err, i, plan), headApplyErr(err, i, plan)
				// a resumed provision: an earlier attempt changed the pool
				first, _ := planAt(errCell{op: c.op})
				g, w := resumeErr(opErr(err, 0, first)), headResumeErr(headApplyErr(err, 0, first))''')
s=s.replace('''	plan := Plan{{Kind: OpSet}, {Kind: c.op}}
	i := 0
	if c.mutated {
		i = 1
	}
	return CodeOf(headApplyErr(err, i, plan))
}''','''	plan, i := planAt(c)
	return CodeOf(headApplyErr(err, i, plan))
}

// planAt is a plan whose op i is c.op: the first op, or the second after a
// set when c is after a change.
func planAt(c errCell) (Plan, int) {
	op := Op{Kind: c.op, Target: "tank/b", Source: "tank/a@s"}
	if c.mutated {
		return Plan{{Kind: OpSet, Target: "tank/a"}, op}, 1
	}
	return Plan{op, {Kind: OpSet, Target: "tank/a"}}, 0
}''')
open(p,'w').write(s)
