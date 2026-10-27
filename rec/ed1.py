import os
os.chdir('/home/user/empty/.claude/worktrees/agent-ad45c3ab7d0b822f7')
p='internal/endpoint/table.go'
s=open(p).read()
old='''// OwnedSnapshot reports'''
new='''// ServedVolume reports whether r is a volume front ends may see and use:
// owned and committed. An uncommitted row is a provision still in flight
// (or abandoned, which Reconcile collects); only CreateVolume's resume and
// DeleteVolume act on it. Every serving path (get, list, expand, export,
// snapshot and clone sources) uses this one rule.
func (r Row) ServedVolume() bool { return r.OwnedVolume() && r.Committed }

// OwnedSnapshot reports'''
assert old in s; s=s.replace(old,new,1); open(p,'w').write(s)

p='internal/endpoint/plan.go'
s=open(p).read()
for old,new in [
 ("!ok || !snap.OwnedSnapshot() || snap.Deferred || !srcOK || !src.OwnedVolume() || !src.Committed","!ok || !snap.OwnedSnapshot() || snap.Deferred || !srcOK || !src.ServedVolume()"),
 ("if !ok || !src.OwnedVolume() || !src.Committed {","if !ok || !src.ServedVolume() {"),
 ('''	row, ok := t.Get(id)
	if !ok || !row.OwnedVolume() {
		return nil, Row{}, errorf(NotFound, "volume %s not found", id)''','''	row, ok := t.Get(id)
	if !ok || !row.ServedVolume() {
		return nil, Row{}, errorf(NotFound, "volume %s not found", id)'''),
 ('''	src, ok := t.Get(vol)
	if !ok || !src.OwnedVolume() {
		return nil, "", errorf(NotFound, "source volume %s not found", vol)''','''	src, ok := t.Get(vol)
	if !ok || !src.ServedVolume() {
		return nil, "", errorf(NotFound, "source volume %s not found", vol)'''),
]:
    assert old in s, old; s=s.replace(old,new)
open(p,'w').write(s)

p='internal/endpoint/endpoint.go'
s=open(p).read()
for old,new in [
 ('''	row, ok := t.Get(id)
	if !ok || !row.OwnedVolume() {
		// only ours''','''	row, ok := t.Get(id)
	if !ok || !row.ServedVolume() {
		// only ours'''),
 ('''	row, ok := t.Get(volID)
	if !ok || !row.OwnedVolume() {
		return Volume{}, errorf(NotFound''','''	row, ok := t.Get(volID)
	if !ok || !row.ServedVolume() {
		return Volume{}, errorf(NotFound'''),
 ("if r.OwnedVolume() && r.Committed {","if r.ServedVolume() {"),
 ('''	t, err := e.scan(ctx, volID)
	if err != nil {
		return "", err
	}
	live, err := e.readEtab(ctx)
	if err != nil {
		return "", err
	}
	plan, row, err := e.plan.PlanPublishExport''','''	t, err := e.scan(ctx, volID)
	if err != nil {
		return "", err
	}
	if row, ok := t.Get(volID); ok && row.OwnedVolume() && !row.ServedVolume() {
		// a provision still in flight is not a volume yet
		return "", errorf(NotFound, "volume %s not found", volID)
	}
	live, err := e.readEtab(ctx)
	if err != nil {
		return "", err
	}
	plan, row, err := e.plan.PlanPublishExport'''),
]:
    assert old in s, old; s=s.replace(old,new)
open(p,'w').write(s)
