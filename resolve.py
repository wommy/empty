import re,sys
p=sys.argv[1]
s=open(p).read()
blocks=re.findall(r'<<<<<<< ours\n(.*?)=======\n(.*?)>>>>>>> theirs\n',s,re.S)
assert len(blocks)==5,len(blocks)
def pick(i,ours,theirs):
    if i==0:
        return """// Mounts follow zfs: create, clone and receive mount a canmount=on
// filesystem (create -u does not); after that only the changelist rules
// move it: a mountpoint change remounts, canmount=off unmounts,
// canmount=on/noauto change nothing, and setting or inheriting a share
// property mounts an unmounted canmount=on filesystem whose share ends up
// on (changelist_postfix). `zfs set -u` does neither. Unmount unmounts.
//
// NFS: sharing a mounted filesystem rewrites /etc/exports.d/zfs.exports
// like libzfs and runs `exportfs -ra`; both are the reference models in
// internal/nfsref (OpenZFS libshare and nfs-utils, derived from their
// sources), so etab shows what a real host shows: "*" for an empty host,
// '@' dropped, crossmnt for "on", first-wins duplicates, client classes
// in mountd order, a malformed line ending the file, realpath. An invalid
// sharenfs is refused like `zfs set` refuses it. Failure modes, each with
// zfs still exiting 0:
"""
    if i==1:
        return theirs  # sharenfs validation moves into parseValue
    if i==2:
        return theirs
    if i==3:
        return theirs.replace("""// reshare brings the served exports of name and its descendants in line
// with their sharenfs (inherited) and mount state, unless NFS is down.
""", """// reshare shares or unshares name and the filesystems below it that
// inherit sharenfs (libzfs changelist_postfix), then runs exportfs.
""")
    if i==4:
        return """	s.ds[name].mounted = false
	if s.unshare(name) {
		s.exportfs()
"""
it=iter(range(5))
s=re.sub(r'<<<<<<< ours\n(.*?)=======\n(.*?)>>>>>>> theirs\n',lambda m:pick(next(it),m.group(1),m.group(2)),s,flags=re.S)
open(p,'w').write(s)
