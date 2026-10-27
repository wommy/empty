import os, sys
out = sys.argv[1]
C = []
def seq(name, covers, desc, *steps):
    C.append((name, covers, desc, steps))

V64 = "67108864"
# A. destroy -r and clones
seq("destroy-r-clone-outside", "destroy-r-clones", "destroy -r refuses a tree whose snapshot has a clone outside it (zfs_main.c destroy_check_dependent)",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs create $R/b", "zfs clone $R/a@s1 $R/b/x", "zfs destroy -r $R/a")
seq("destroy-r-clone-inside", "destroy-r-clones", "destroy -r succeeds when the clone lives inside the tree (dependents destroyed post-order)",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/a/x", "zfs destroy -r $R/a")
seq("destroy-snap-cloned", "destroy-r-clones", "plain destroy of a cloned snapshot lists the clones",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/x", "zfs destroy $R/a@s1")
seq("destroy-r-vol-clone", "destroy-r-clones", "destroy -r of a zvol whose snapshot backs a clone volume (the endpoint's clone-from-snapshot layout)",
    f"zfs create -V {V64} $R/v", "zfs snapshot $R/v@s1", "zfs clone $R/v@s1 $R/x", "zfs destroy -r $R/v")
seq("destroy-children", "destroy-r-clones", "destroy without -r of a dataset with children: 'filesystem has children' lists only direct children",
    "zfs create $R/a", "zfs create $R/a/c", "zfs create $R/a/c/d", "zfs snapshot $R/a@s1", "zfs destroy $R/a")
seq("destroy-vol-snapshots", "destroy-r-clones", "destroy without -r of a volume with snapshots: 'volume has children'",
    f"zfs create -V {V64} $R/v", "zfs snapshot $R/v@s1", "zfs destroy $R/v")
seq("destroy-r-deferred-origin-inside", "destroy-r-clones", "destroy -r over a deferred origin and its clone in the same tree (crashed the HEAD simulator)",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/a/x", "zfs destroy -d $R/a@s1", "zfs destroy -r $R/a")
seq("destroy-r-held-child", "destroy-r-clones", "destroy -r with a held snapshot: the walk destroys what it can and reports busy",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs create $R/a/x", "zfs hold t1 $R/a@s1", "zfs destroy -r $R/a", "zfs release t1 $R/a@s1", "zfs destroy -r $R/a")
seq("destroy-R-snap", "destroy-r-clones", "destroy -R destroys clones then the snapshot (not in the simulator)",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/x", "zfs destroy -R $R/a@s1")
seq("destroy-r-snapshot-recursive", "destroy-r-clones", "destroy -r fs@snap reaches descendants' snapshots even when fs@snap is missing",
    "zfs create $R/a", "zfs create $R/a/c", "zfs snapshot $R/a/c@s1", "zfs destroy -r $R/a@s1", "zfs destroy -r $R/a@s1")
seq("destroy-missing", "destroy-r-clones", "destroy of missing datasets and snapshots: the not-found texts the endpoint's OkMissing relies on",
    "zfs destroy $R/nope", "zfs destroy -r $R/nope", "zfs destroy $R/nope@s1", "zfs create $R/a", "zfs destroy $R/a@nope", "zfs destroy -d $R/nope@s1")
# B. destroy -d
seq("defer-cloned-then-clone-destroyed", "destroy-d", "destroy -d marks a cloned snapshot; destroying the clone reaps it (dsl_destroy.c rmorigin)",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/x", "zfs destroy -d $R/a@s1", "zfs destroy $R/x")
seq("defer-unreferenced", "destroy-d", "destroy -d of a snapshot nothing references destroys it at once",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs destroy -d $R/a@s1")
seq("defer-held-release", "destroy-d", "destroy -d of a held snapshot defers; the last release destroys it",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs hold t1 $R/a@s1", "zfs destroy -d $R/a@s1", "zfs release t1 $R/a@s1")
seq("defer-on-filesystem", "destroy-d", "destroy -d of a filesystem: not a snapshot name",
    "zfs create $R/a", "zfs destroy -d $R/a")
seq("defer-two-clones", "destroy-d", "a deferred origin with two clones goes only with the last clone",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/x", "zfs clone $R/a@s1 $R/y", "zfs destroy -d $R/a@s1", "zfs destroy $R/x", "zfs destroy $R/y")
seq("defer-held-and-cloned", "destroy-d", "a deferred origin that is also held survives its clone and goes with the release",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/x", "zfs hold t1 $R/a@s1", "zfs destroy -d $R/a@s1", "zfs destroy $R/x", "zfs release t1 $R/a@s1")
seq("defer-recursive", "destroy-d", "destroy -rd marks every descendant's snapshot that is held or cloned",
    "zfs create $R/a", "zfs create $R/a/c", "zfs snapshot -r $R/a@s1", "zfs hold t1 $R/a/c@s1", "zfs destroy -rd $R/a@s1")
seq("defer-missing", "destroy-d", "destroy -d of a missing snapshot",
    "zfs create $R/a", "zfs destroy -d $R/a@s1")
seq("defer-endpoint-clone-vol", "destroy-d", "endpoint layout: zvol clone of an internal snapshot, origin destroy -d, then clone destroy -r reaps the origin",
    f"zfs create -V {V64} $R/v", "zfs snapshot $R/v@s1", "zfs clone -o refreservation=auto $R/v@s1 $R/x", "zfs destroy -d $R/v@s1", "zfs destroy -r $R/x")
seq("defer-snapshot-of-promoted", "destroy-d", "deferred origin and promote: after promote the old origin belongs to the clone",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/x", "zfs destroy -d $R/a@s1", "zfs promote $R/x", "zfs destroy $R/a")
# C. create -p
seq("create-p-existing-fs", "create-p", "create -p on an existing filesystem succeeds and sets nothing",
    "zfs create $R/a", "zfs create -p -o zd:a=1 $R/a")
seq("create-p-fs-over-vol", "create-p", "create -p of a filesystem where a volume exists: the existence check is type-specific (zfs_main.c zfs_dataset_exists(name, type))",
    f"zfs create -V {V64} $R/v", "zfs create -p $R/v")
seq("create-p-vol-over-fs", "create-p", "create -p -V where a filesystem exists",
    "zfs create $R/a", f"zfs create -p -V {V64} $R/a")
seq("create-p-vol-existing", "create-p", "create -p -V on an existing volume succeeds",
    f"zfs create -V {V64} $R/v", f"zfs create -p -V {V64} $R/v")
seq("create-p-ancestors", "create-p", "create -p makes and mounts missing ancestors",
    "zfs create -p $R/a/b/c", "zfs create -p -o canmount=noauto $R/d/e")
seq("create-p-under-volume", "create-p", "create -p below a volume: parent is not a filesystem",
    f"zfs create -V {V64} $R/v", "zfs create -p $R/v/x", "zfs create $R/v/x")
seq("create-missing-parent", "create-p", "create without -p under a missing parent",
    "zfs create $R/a/b")
seq("create-existing", "create-p", "create without -p of an existing dataset",
    "zfs create $R/a", "zfs create $R/a", f"zfs create -V {V64} $R/a")
seq("create-s-without-V", "create-p", "-s without -V is a usage error (exit 2)",
    "zfs create -s $R/a")
# D. volumes / refreservation=auto
seq("create-vol-resv-auto", "resv-auto", "AMBIGUOUS: create -V -o refreservation=auto. 2.4.4 has no zfs_create_fix_auto_resv: UINT64_MAX reaches the kernel (ENOSPC); the simulator resolves it",
    f"zfs create -V {V64} -o refreservation=auto $R/v")
seq("set-resv-auto-sparse", "resv-auto", "set refreservation=auto on a sparse volume resolves to the full reservation (zfs_fix_auto_resv)",
    f"zfs create -s -V {V64} $R/v", "zfs set refreservation=auto $R/v")
seq("clone-resv-auto", "resv-auto", "clone -o refreservation=auto of a thick volume (the endpoint's sizeOps): resolved against the snapshot",
    f"zfs create -V {V64} $R/v", "zfs snapshot $R/v@s1", "zfs set volsize=134217728 $R/v", "zfs clone -o refreservation=auto $R/v@s1 $R/x")
seq("clone-vol-default-resv", "resv-auto", "a volume clone without options has no refreservation",
    f"zfs create -V {V64} $R/v", "zfs snapshot $R/v@s1", "zfs clone $R/v@s1 $R/x")
seq("set-resv-auto-fs", "resv-auto", "refreservation=auto on a filesystem",
    "zfs create $R/a", "zfs set refreservation=auto $R/a", "zfs create -o refreservation=auto $R/b")
seq("volsize-tracks-thick", "resv-auto", "a thick volume's refreservation follows volsize (zfs_add_synthetic_resv)",
    f"zfs create -V {V64} $R/v", "zfs set volsize=134217728 $R/v")
seq("volsize-sparse", "resv-auto", "a sparse volume keeps refreservation 0 on resize",
    f"zfs create -s -V {V64} $R/v", "zfs set volsize=134217728 $R/v")
seq("volsize-custom-resv", "resv-auto", "a custom refreservation does not track volsize",
    f"zfs create -V {V64} -o refreservation=10485760 $R/v", "zfs set volsize=134217728 $R/v")
seq("volsize-and-auto", "resv-auto", "volsize and refreservation=auto in one set: auto uses the new size",
    f"zfs create -s -V {V64} $R/v", "zfs set volsize=134217728 refreservation=auto $R/v")
seq("volsize-not-multiple", "resv-auto", "volsize must be a multiple of volblocksize; 0 is refused",
    f"zfs create -V {V64} $R/v", "zfs set volsize=1000000 $R/v", "zfs set volsize=0 $R/v")
seq("create-vol-rounding", "resv-auto", "create -V rounds up to volblocksize; -V 0 is refused",
    "zfs create -V 1000000 $R/v", "zfs create -V 0 $R/w", "zfs create -s -V 1000000 -o volblocksize=65536 $R/x")
seq("volsize-enospc-revert", "resv-auto", "growing a thick volume past the pool: ENOSPC on the synthetic refreservation and volsize is put back",
    f"zfs create -V {V64} $R/v", "zfs set volsize=21474836480 $R/v")
seq("volblocksize-rules", "resv-auto", "volblocksize: power of 2 at create, readonly after (set, clone -o, inherit)",
    "zfs create -s -V 1000000 -o volblocksize=3000 $R/v", f"zfs create -V {V64} -o volblocksize=8192 $R/v", "zfs set volblocksize=16384 $R/v",
    "zfs snapshot $R/v@s1", "zfs clone -o volblocksize=16384 $R/v@s1 $R/x", "zfs inherit volblocksize $R/v")
seq("zvol-snapshot-volsize", "resv-auto", "a zvol snapshot keeps its volsize when the volume grows (known simulator gap at HEAD), source local",
    f"zfs create -V {V64} $R/v", "zfs snapshot $R/v@s1", "zfs set volsize=134217728 $R/v")
seq("thick-vol-too-big", "resv-auto", "a thick volume larger than the pool fails out of space; sparse succeeds",
    "zfs create -V 21474836480 $R/v", "zfs create -s -V 21474836480 $R/w")
# E. received vs local
seq("recv-user-prop", "received", "send -p | recv makes the source's local properties received on the target",
    "zfs create -o zd:a=1 $R/a", "zfs snapshot $R/a@s1", "@sendrecv $R/a@s1 $R/r")
seq("recv-local-over-received", "received", "a local value shadows a received one; plain inherit hides the received value too ($inherit)",
    "zfs create -o zd:a=1 $R/a", "zfs snapshot $R/a@s1", "@sendrecv $R/a@s1 $R/r", "zfs set zd:a=2 $R/r", "zfs inherit zd:a $R/r")
seq("recv-inherit-S", "received", "inherit -S reverts to the received value",
    "zfs create -o zd:a=1 $R/a", "zfs snapshot $R/a@s1", "@sendrecv $R/a@s1 $R/r", "zfs set zd:a=2 $R/r", "zfs inherit -S zd:a $R/r")
seq("recv-sharenfs", "received", "received sharenfs (the endpoint refuses to override it): set local, plain inherit, inherit -S",
    "zfs create -o sharenfs=rw=@10.0.0.0/8 $R/a", "zfs snapshot $R/a@s1", "@sendrecv $R/a@s1 $R/r", "zfs set sharenfs=off $R/r", "zfs inherit sharenfs $R/r", "zfs inherit -S sharenfs $R/r")
seq("recv-inherited-by-child", "received", "a received value is inherited by children ('inherited from' the received dataset)",
    "zfs create -o zd:b=x $R/a", "zfs snapshot $R/a@s1", "@sendrecv $R/a@s1 $R/r", "zfs create $R/r/c", "zfs snapshot $R/r/c@s2")
seq("recv-vol-refreservation", "received", "a thick volume's refreservation arrives received; resize keeps it tracking",
    f"zfs create -V {V64} $R/v", "zfs snapshot $R/v@s1", "@sendrecv $R/v@s1 $R/r", "zfs set volsize=134217728 $R/r")
seq("recv-canmount-off", "received", "a received canmount=off leaves the target unmounted",
    "zfs create -o canmount=off $R/a", "zfs snapshot $R/a@s1", "@sendrecv $R/a@s1 $R/r")
seq("recv-inherit-native", "received", "plain inherit of a received native property falls back to the pool's lz4, not the received value",
    "zfs create -o compression=gzip-6 $R/a", "zfs snapshot $R/a@s1", "@sendrecv $R/a@s1 $R/r", "zfs inherit compression $R/r", "zfs inherit -S compression $R/r")
seq("recv-existing", "received", "recv onto an existing dataset without -F",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs create $R/r", "@sendrecv $R/a@s1 $R/r")
seq("recv-quota-none", "received", "received refquota, then set refquota=none locally",
    "zfs create -o refquota=1073741824 $R/a", "zfs snapshot $R/a@s1", "@sendrecv $R/a@s1 $R/r", "zfs set refquota=none $R/r", "zfs inherit -S refquota $R/r")
# F. set -u
seq("set-u-sharenfs-mounted", "set-u", "set -u sharenfs on a mounted filesystem: still mounted",
    "zfs create $R/a", "zfs set -u sharenfs=on $R/a")
seq("set-u-mountpoint", "set-u", "set -u mountpoint: no unmount or remount, the new value is reported",
    "zfs create $R/a", "zfs set -u mountpoint=none $R/a")
seq("set-u-canmount-off", "set-u", "set -u canmount=off leaves a mounted filesystem mounted",
    "zfs create $R/a", "zfs set -u canmount=off $R/a")
seq("set-u-unmounted-share", "set-u", "set -u sharenfs on an unmounted filesystem leaves it unmounted; a plain set then mounts it (the endpoint's SetNoMount)",
    "zfs create -u $R/a", "zfs set -u sharenfs=rw=@10.0.0.0/8 $R/a", "zfs set sharenfs=rw=@10.0.0.0/16 $R/a")
seq("set-u-user-prop", "set-u", "set -u of a user property",
    "zfs create -u $R/a", "zfs set -u zd:a=1 $R/a")
seq("set-u-volsize", "set-u", "set -u on a volume",
    f"zfs create -V {V64} $R/v", "zfs set -u volsize=134217728 $R/v")
# G. sharenfs on unmounted
seq("share-unmounted-mounts", "sharenfs-unmounted", "plain set sharenfs on an unmounted canmount=on filesystem mounts it (changelist_postfix)",
    "zfs create -u $R/a", "zfs set sharenfs=on $R/a")
seq("share-off-unmounted", "sharenfs-unmounted", "sharenfs=off on an unmounted filesystem does not mount it",
    "zfs create -u $R/a", "zfs set sharenfs=off $R/a")
seq("share-noauto", "sharenfs-unmounted", "canmount=noauto: sharenfs does not mount",
    "zfs create -o canmount=noauto $R/a", "zfs set sharenfs=on $R/a")
seq("share-children", "sharenfs-unmounted", "sharenfs on a parent mounts unmounted children that inherit it",
    "zfs create -u $R/a", "zfs create -u $R/a/c", "zfs create -u -o sharenfs=off $R/a/d", "zfs set sharenfs=on $R/a")
seq("share-mountpoint-none", "sharenfs-unmounted", "mountpoint=none: sharenfs cannot mount",
    "zfs create -o mountpoint=none $R/a", "zfs set sharenfs=on $R/a")
seq("share-other-prop", "sharenfs-unmounted", "a non-share property set does not mount an unmounted filesystem even if shared",
    "zfs create -u -o sharenfs=on $R/a", "zfs set zd:a=1 $R/a", "zfs set compression=off $R/a")
seq("share-inherit", "sharenfs-unmounted", "inherit sharenfs on an unmounted filesystem whose parent shares mounts it",
    "zfs create -o sharenfs=on $R/a", "zfs create -u -o sharenfs=off $R/a/c", "zfs inherit sharenfs $R/a/c")
seq("canmount-on-unmounted", "sharenfs-unmounted", "canmount=on does not mount (no changelist for on/noauto)",
    "zfs create -o canmount=noauto $R/a", "zfs set canmount=on $R/a")
seq("canmount-noauto-mounted", "sharenfs-unmounted", "canmount=noauto on a mounted filesystem leaves it mounted",
    "zfs create $R/a", "zfs set canmount=noauto $R/a", "zfs set canmount=off $R/a")
seq("mountpoint-remount", "sharenfs-unmounted", "mountpoint none then back: unmounted then mounted again, children follow",
    "zfs create $R/a", "zfs create $R/a/c", "zfs set mountpoint=none $R/a", "zfs inherit mountpoint $R/a")
seq("create-u-shared", "sharenfs-unmounted", "create -u with sharenfs=on: not mounted",
    "zfs create -u -o sharenfs=on $R/a")
# H. holds
seq("hold-basic", "holds", "hold, duplicate tag, second tag, userrefs",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs hold t1 $R/a@s1", "zfs hold t1 $R/a@s1", "zfs hold t2 $R/a@s1")
seq("hold-destroy", "holds", "destroying a held snapshot: EBUSY 'it's being held'",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs hold t1 $R/a@s1", "zfs destroy $R/a@s1", "zfs destroy -r $R/a")
seq("release-missing", "holds", "release of a tag that is not there, of a missing snapshot, of a non-snapshot",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs release t1 $R/a@s1", "zfs release t1 $R/a@s2", "zfs release t1 $R/a", "zfs hold t1 $R/a")
seq("hold-recursive", "holds", "hold -r and release -r across children",
    "zfs create $R/a", "zfs create $R/a/c", "zfs snapshot -r $R/a@s1", "zfs hold -r t1 $R/a@s1", "zfs release -r t1 $R/a@s1")
seq("release-r-partial", "holds", "release -r where a child lacks the snapshot",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs create $R/a/c", "zfs hold t1 $R/a@s1", "zfs release -r t1 $R/a@s1")
# I. inherit
seq("inherit-native-from-pool", "inherit", "native inheritance from the pool root (compression=lz4 from zpool create -O)",
    "zfs create $R/a", "zfs create -V 67108864 $R/a/w", "zfs set compression=off $R/a", "zfs create $R/a/c", "zfs inherit compression $R/a")
seq("inherit-recursive", "inherit", "inherit -r clears children and skips types the property does not apply to",
    "zfs create -o zd:a=1 $R/a", "zfs create -o zd:a=2 $R/a/c", "zfs create -V 67108864 -o zd:a=3 $R/a/w", "zfs inherit -r zd:a $R/a", "zfs set atime=off $R/a/c", "zfs inherit -r atime $R/a")
seq("inherit-noninheritable", "inherit", "inherit of a non-inheritable, readonly or unknown property",
    "zfs create $R/a", "zfs inherit refquota $R/a", "zfs inherit canmount $R/a", "zfs inherit type $R/a", "zfs inherit bogus $R/a", "zfs inherit -S refquota $R/a")
seq("inherit-wrong-type", "inherit", "inherit of a filesystem property on a volume",
    "zfs create -V 67108864 $R/v", "zfs inherit atime $R/v", "zfs inherit sharenfs $R/v")
seq("inherit-snapshot-user", "inherit", "user properties on snapshots: set, inherited from the dataset, inherit",
    "zfs create -o zd:b=x $R/a", "zfs snapshot -o zd:a=s $R/a@s1", "zfs inherit zd:a $R/a@s1", "zfs set zd:b=y $R/a@s1")
# J. quotas / reservations
seq("refquota-zero", "quota", "refquota=0 must be spelled none; none is stored as a local 0",
    "zfs create $R/a", "zfs set refquota=0 $R/a", "zfs set refquota=none $R/a", "zfs set quota=none $R/a")
seq("refquota-below-resv", "quota", "refquota below refreservation is ENOSPC 'size is less than current used or reserved space'",
    "zfs create -o refreservation=10485760 $R/a", "zfs set refquota=1048576 $R/a")
seq("expand-pattern", "quota", "the endpoint's Expand: refquota and refreservation in one set, keys sorted (refquota first)",
    "zfs create -o refquota=10485760 -o refreservation=10485760 $R/a", "zfs set refquota=20971520 refreservation=20971520 $R/a")
seq("shrink-pattern", "quota", "shrinking refquota and refreservation together: refquota is set first and fails against the old refreservation (partial apply)",
    "zfs create -o refquota=20971520 -o refreservation=20971520 $R/a", "zfs set refquota=10485760 refreservation=10485760 $R/a")
seq("resv-over-quota", "quota", "a child's refreservation beyond its parent's quota",
    "zfs create -o quota=1048576 $R/a", "zfs create $R/a/c", "zfs set refreservation=10485760 $R/a/c", "zfs create -o refreservation=10485760 $R/a/d")
seq("resv-too-big", "quota", "refreservation larger than the pool: 'size is greater than available space'; at create: out of space",
    "zfs create $R/a", "zfs set refreservation=21474836480 $R/a", "zfs create -o refreservation=21474836480 $R/b")
seq("resv-over-refquota", "quota", "refreservation above refquota on a filesystem",
    "zfs create -o refquota=10485760 $R/a", "zfs set refreservation=20971520 $R/a")
seq("quota-below-child-resv", "quota", "quota below the reservations already under it",
    "zfs create $R/a", "zfs create -o refreservation=10485760 $R/a/c", "zfs set quota=1048576 $R/a")
seq("partial-set", "quota", "a set that fails one space property still applies the others",
    "zfs create $R/a", "zfs set refreservation=21474836480 zd:a=1 atime=off $R/a")
# K. promote / snapshot / misc
seq("promote-basic", "promote", "promote moves the origin and older snapshots; the old head becomes a clone",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs snapshot $R/a@s2", "zfs snapshot $R/a@s3", "zfs clone $R/a@s2 $R/x", "zfs promote $R/x")
seq("promote-conflict", "promote", "promote with a conflicting snapshot name",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/x", "zfs snapshot $R/x@s1", "zfs promote $R/x")
seq("promote-not-clone", "promote", "promote of a dataset that is not a clone; promote back",
    "zfs create $R/a", "zfs promote $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s1 $R/x", "zfs promote $R/x", "zfs promote $R/a")
seq("snapshot-errors", "snapshot", "snapshot of a missing dataset (usage, exit 2), with a native -o, twice",
    "zfs snapshot $R/a@s1", "zfs create $R/a", "zfs snapshot -o compression=off $R/a@s1", "zfs snapshot $R/a@s1", "zfs snapshot $R/a@s1")
seq("snapshot-r-exists", "snapshot", "snapshot -r is atomic: one existing name creates none",
    "zfs create $R/a", "zfs create $R/a/c", "zfs snapshot $R/a/c@s1", "zfs snapshot -r $R/a@s1")
seq("set-snapshot-native", "snapshot", "a native property on a snapshot cannot be set",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs set compression=off $R/a@s1", "zfs set -u dedup=off $R/a@s1")
seq("set-errors", "misc", "set errors: wrong type, bad index value, bad block size, duplicate key (exit 255)",
    "zfs create $R/a", "zfs set volsize=67108864 $R/a", "zfs set compression=bogus $R/a", "zfs set recordsize=3000 $R/a", "zfs set atime=off atime=on $R/a")
seq("clone-errors", "misc", "clone of a missing snapshot, onto an existing name, under a missing parent, wrong-type property",
    "zfs create $R/a", "zfs snapshot $R/a@s1", "zfs clone $R/a@s2 $R/x", "zfs clone $R/a@s1 $R/a", "zfs clone $R/a@s1 $R/n/x", "zfs clone -o volsize=67108864 $R/a@s1 $R/x")
seq("mountpoint-inherit-path", "misc", "an inherited local mountpoint is the setpoint path plus the relative name",
    "zfs create -o mountpoint=none $R/a", "zfs create $R/a/c", "zfs set mountpoint=legacy $R/a", "zfs inherit mountpoint $R/a")
seq("xattr-acltype-index", "misc", "index values read back canonical: xattr=on is sa, acltype=posix is posix",
    "zfs create -o xattr=on -o acltype=posixacl $R/a", "zfs snapshot $R/a@s1", "zfs set compression=gzip-6 $R/a")
seq("zvol-snapshot-fs-props", "misc", "a zvol snapshot prints '-' for filesystem-only properties (headcheck)",
    f"zfs create -V {V64} $R/v", "zfs snapshot $R/v@s1")

os.makedirs(out, exist_ok=True)
for f in os.listdir(out):
    if f.endswith(".txt"):
        os.remove(os.path.join(out, f))
for name, covers, desc, steps in C:
    with open(os.path.join(out, name + ".txt"), "w") as fh:
        fh.write(f"# {desc}\n# covers: {covers}\n")
        for s in steps:
            fh.write(s + "\n")
print(len(C))
