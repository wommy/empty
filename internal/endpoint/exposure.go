package endpoint

// ExposedByAncestor lists the etab lines that export every volume under
// mount, the parent's mountpoint: a crossmnt line of mount itself or of
// any ancestor. mountd follows crossmnt into each filesystem mounted
// below the line's path (nfs-utils cache.c path_matches), so such a line
// serves a volume to its clients from the moment the volume is created,
// whatever the volume's own sharenfs; Publish's check (exposure) sees it
// only later. An inherited `sharenfs=on` is "rw,crossmnt" to "*" (libshare),
// so a pool shared with `zfs set sharenfs=on tank` is one (ADR 0001 v2, F5).
//
// mount is resolved like exportfs resolves an export path (realpath).
// The result is sorted; nil means no such line.
func ExposedByAncestor(etab []byte, mount string) []string {
	t := ParseEtab(etab)
	p := realPath(mount)
	var out []string
	for _, c := range t[p] {
		if c.crossmnt {
			out = append(out, etabLine{p, c}.String())
		}
	}
	for _, l := range t.crossmntAncestors(p) {
		out = append(out, l.String())
	}
	return out
}
