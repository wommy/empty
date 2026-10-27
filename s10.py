p = '/home/user/empty/.claude/worktrees/agent-a5205796387710ae5/cmd/zfs-endpoint/startup.go'
s = open(p).read()
s += '''
// legacyPrefix is the property namespace builds before 7f5cb39 wrote by
// default; -property-prefix democratic-csi still adopts its volumes.
const legacyPrefix = "democratic-csi"

// checkLegacyPrefix refuses (78) to serve a parent whose volumes all carry
// legacyPrefix's managed marker and none carry prefix's: an upgrade across
// the default's change, without the flag. Served, every such volume reads
// as not the endpoint's: ListVolumes is empty, DeleteVolume is
// FAILED_PRECONDITION and a CreateVolume retry ALREADY_EXISTS. That fails
// safe, but it is an outage nobody asked for.
//
// It is one `zfs get -d 1` of the two markers on the parent's children
// (volumes are direct children), bounded like every startup probe. Only
// local values count, as for ownership (ADR 0003). A parent with no
// volumes, or with any of prefix's, passes.
func checkLegacyPrefix(ctx context.Context, z *zfs.Client, parent, prefix string) error {
	if prefix == legacyPrefix {
		return nil
	}
	ours, old := prefix+":managed_resource", legacyPrefix+":managed_resource"
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	children, err := z.List(ctx, parent, zfs.ListOptions{Props: []string{ours, old}})
	switch {
	case cannotRun(err):
		return configErr("cannot run zfs: %v", err)
	case err != nil:
		return tempErr("scan %s for volumes under another property prefix: %v", parent, err)
	}
	legacy := 0
	for _, c := range children {
		if marked(c, ours) {
			return nil
		}
		if marked(c, old) {
			legacy++
		}
	}
	if legacy > 0 {
		return configErr("%d datasets under %s carry %s and none carry %s: they were provisioned under the old default prefix; "+
			"pass -property-prefix %s to keep serving them, or -property-prefix %s to start a new namespace beside them",
			legacy, parent, old, ours, legacyPrefix, prefix)
	}
	return nil
}

// marked reports a locally set "true" marker.
func marked(d zfs.Dataset, prop string) bool {
	p, ok := d.Props[prop]
	return ok && p.Local() && p.Value == "true"
}
'''
open(p, 'w').write(s)

p = '/home/user/empty/.claude/worktrees/agent-a5205796387710ae5/cmd/zfs-endpoint/main.go'
s = open(p).read()
old = '''	if err != nil {
		return configErr("%v", err)
	}
	if !root {'''
new = '''	if err != nil {
		return configErr("%v", err)
	}
	// an explicit -property-prefix is the operator's choice; the default
	// is checked for an upgrade across its change
	explicitPrefix := false
	fs.Visit(func(f *flag.Flag) { explicitPrefix = explicitPrefix || f.Name == "property-prefix" })
	if !explicitPrefix {
		if err := checkLegacyPrefix(ctx, z, *parent, *prefix); err != nil {
			return startupErr(ctx, err)
		}
	}
	if !root {'''
assert old in s
s = s.replace(old, new)
open(p, 'w').write(s)
