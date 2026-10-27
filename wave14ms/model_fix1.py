W = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/'


def edit(f, pairs):
    p = W + f
    s = open(p).read()
    for a, b in pairs:
        assert s.count(a) == 1, (f, a)
        s = s.replace(a, b)
    open(p, 'w').write(s)


edit('model_test.go', [
    ('''// refuses is what DeleteVolume judges from the scan, before any op: the
// refusals that leave every dataset unchanged.
func (m *Model) refuses(id string, destroyForeign bool) endpoint.Code {
	for _, s := range m.snapsOf(id) {''', '''// refuses is what DeleteVolume judges from the scan, before any op: the
// refusals that leave every dataset unchanged. A dataset below the volume
// (OWN-12) and a user hold (REC-11, HOLD-REPLAN) are refused here, not left
// to zfs, so a reconcile never re-plans what zfs refuses every time.
func (m *Model) refuses(id string, destroyForeign bool) endpoint.Code {
	if len(m.children(id)) > 0 {
		return endpoint.FailedPrecondition
	}
	for _, s := range m.snapsOf(id) {'''),
    ('''		case m.hasClones(s): // DEL-03
			return endpoint.FailedPrecondition''', '''		case m.hasClones(s): // DEL-03
			return endpoint.FailedPrecondition
		case sn.Holds > 0: // REC-11
			return endpoint.FailedPrecondition'''),
])
edit('modelrun_test.go', [
    ('''	{id: "OWN-12", row: "OWN-12", expires: "2026-12-15",
		why: "the scan drops rows below a volume, so reconcile re-plans a destroy zfs refuses (has children) on every run",
		match: func(m *Model, st Step) (bool, bool) {
			if st.Op != "Reconcile" {
				return false, false
			}
			for k := range m.DS {
				if v, _, nested := strings.Cut(k, "/"); nested && m.ownedVol(v) && !slices.Contains(st.Desired, v) {
					return true, false
				}
			}
			return false, false
		}},
	{id: "HOLD-REPLAN", row: "REC-11", expires: "2026-12-15",
		why: "the scan does not read userrefs, so reconcile re-plans a destroy of a held snapshot that zfs refuses (busy) on every run",
		match: func(m *Model, st Step) (bool, bool) {
			if st.Op != "Reconcile" {
				return false, false
			}
			for k, sn := range m.Snaps {
				if v, _, _ := strings.Cut(k, "@"); sn.Holds > 0 && m.ownedVol(v) && !slices.Contains(st.Desired, v) {
					return true, false
				}
			}
			return false, false
		}},
''', ''),
])
