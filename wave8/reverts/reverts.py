#!/usr/bin/env python3
"""Re-introduce one fixed bug at a time in the model copy.
usage: reverts.py <copy-dir> <id>    (git checkout restores)"""
import sys, os
REV = {
 "R1": ("35fa482 clone never adopts a racing target (OpClone OkExists)", [
   ("internal/endpoint/plan.go",
    "plan = Plan{{Kind: OpClone, Source: snap.Name, Target: p.full(id), Props: own}}",
    "plan = Plan{{Kind: OpClone, Source: snap.Name, Target: p.full(id), Props: own, OkExists: true}}"),
   ("internal/endpoint/plan.go",
    "{Kind: OpClone, Source: internal, Target: p.full(id), Props: own},",
    "{Kind: OpClone, Source: internal, Target: p.full(id), Props: own, OkExists: true},"),
 ]),
 "R2": ("5e5b2eb received properties are not ownership", [
   ("internal/zfs/zfs.go",
    'func (p Prop) Local() bool { return p.Source == "local" }',
    'func (p Prop) Local() bool { return p.Source == "local" || p.Source == "received" }'),
 ]),
 "R3": ("16a356c clone only from owned, committed sources", [
   ("internal/endpoint/plan.go",
    "if !ok || !snap.OwnedSnapshot() || snap.Deferred || !srcOK || !src.OwnedVolume() || !src.Committed {",
    "if !ok || snap.Type != zfs.TypeSnapshot || snap.Deferred {"),
   ("internal/endpoint/plan.go",
    """		if src.Kind() != r.Kind {
			return nil, errorf(InvalidArgument, "source is %s, requested %s", src.Kind(), r.Kind)
		}
		if sizeErr != nil {
			return nil, sizeErr
		}
		if err := cloneBlockSize(own, src); err != nil {
			return nil, err
		}
		own[p.Names.sourceType()], own[p.Names.sourceID()] = srcType, srcID""",
    """		if srcOK && src.Kind() != r.Kind {
			return nil, errorf(InvalidArgument, "source is %s, requested %s", src.Kind(), r.Kind)
		}
		if sizeErr != nil {
			return nil, sizeErr
		}
		if err := cloneBlockSize(own, src); err != nil {
			return nil, err
		}
		own[p.Names.sourceType()], own[p.Names.sourceID()] = srcType, srcID"""),
   ("internal/endpoint/plan.go",
    "if !ok || !src.OwnedVolume() || !src.Committed {",
    "if !ok || src.Type == zfs.TypeSnapshot {"),
 ]),
 "R4": ("725c328 size clones once, before the resume branch", [
   ("internal/endpoint/plan.go",
    """		if !row.Committed {
			if sizeErr != nil {
				return nil, sizeErr
			}""",
    """		if !row.Committed {"""),
   ("internal/endpoint/plan.go",
    "plan := p.sizeOps(id, row, r, size)",
    "plan := p.sizeOps(id, row, r, want)"),
   ("internal/endpoint/plan.go",
    "plan = append(plan, p.sizeOps(id, Row{Type: kindType(r.Kind), Volsize: snap.Volsize}, r, size)...)",
    """plan = append(plan, p.sizeOps(id, Row{Type: kindType(r.Kind), Volsize: snap.Volsize}, r, want)...)
		if r.Kind == Filesystem && want == 0 && src.Refquota > 0 {
			plan = append(plan, Op{Kind: OpSet, Target: p.full(id), Props: map[string]string{"refquota": u64(src.Refquota)}})
		}"""),
   ("internal/endpoint/plan.go",
    """		base := src
		// a clone""",
    """		base := src
		if r.Kind == Filesystem && want == 0 {
			want = src.Refquota
		}
		size = want
		// a clone"""),
 ]),
 "R5": ("d4ec271 DeleteVolume spares snapshots the endpoint did not make", [
   ("internal/endpoint/plan.go",
    "if len(foreign) > 0 && !p.DestroyForeignSnapshots {",
    "if false && len(foreign) > 0 && !p.DestroyForeignSnapshots {"),
 ]),
 "R6": ("6d4043a map busy and unknown failures by operation", [
   ("internal/endpoint/apply.go",
    """	switch {
	case op.Kind == OpDestroy && errors.Is(err, zfs.ErrBusy):""",
    """	switch {
	case true:
	case op.Kind == OpDestroy && errors.Is(err, zfs.ErrBusy):"""),
 ]),
 "R7": ("0bd51b0 CSI ids are opaque (never-issued ids)", [
   ("internal/endpoint/endpoint.go",
    """func issuedVolumeID(id string) (string, error) {""",
    """func issuedVolumeID(id string) (string, error) {
	if true {
		return volumeID(id)
	}"""),
 ]),
 "R8": ("08b9699 class: CreateSnapshot judges global names from a partial table", [
   ("internal/endpoint/endpoint.go",
    """	// names are global, so the whole parent is the input
	t, err := e.scan(ctx, "")""",
    """	// names are global, so the whole parent is the input
	t, err := e.scan(ctx, sourceVolumeID)"""),
   ("internal/endpoint/table.go",
    """func (t Table) mustBeWhole(what string) error {""",
    """func (t Table) mustBeWhole(what string) error {
	if true {
		return nil
	}"""),
 ]),
 "R9": ("35fa482 ownership requires the volume-name/snapshot-source marker to match", [
   ("internal/endpoint/table.go",
    "return r.Type != zfs.TypeSnapshot && r.Managed && (r.VolumeName == \"\" || r.VolumeName == r.ID)",
    "return r.Type != zfs.TypeSnapshot && r.Managed"),
   ("internal/endpoint/table.go",
    """	return r.Type == zfs.TypeSnapshot && r.Managed && r.SnapshotName != "" &&
		(r.SnapshotSource == "" || r.SnapshotSource == vol)""",
    """	_ = vol
	return r.Type == zfs.TypeSnapshot && r.Managed && r.SnapshotName != \"\""""),
 ]),
 "R10": ("2fadc0c a killed zfs command is Interrupted (retryable), never Internal", [
   ("internal/zfs/zfs.go",
    "	if cerr := ctx.Err(); cerr != nil {",
    "	if cerr := ctx.Err(); false && cerr != nil {"),
 ]),
}
if __name__ == "__main__":
    d, rid = sys.argv[1], sys.argv[2]
    if rid == "list":
        for k, (desc, _) in REV.items(): print(k, desc)
        sys.exit(0)
    for f, a, b in REV[rid][1]:
        p = os.path.join(d, f)
        s = open(p).read()
        assert s.count(a) == 1, (rid, f, a[:60], s.count(a))
        open(p, "w").write(s.replace(a, b))
    print(rid, REV[rid][0])
