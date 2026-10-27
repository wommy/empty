def edit(p, pairs):
    s = open(p).read()
    for a, b in pairs:
        assert s.count(a) == 1, (p, a)
        s = s.replace(a, b)
    open(p, 'w').write(s)
edit('internal/endpoint/plan.go', [
("""	// RequireMounted: the endpoint can mount (it runs as root), so a
	// filesystem volume zfs would mount is committed only once a scan
	// shows it mounted. OpenZFS 2.4's `zfs create` and `zfs clone` exit 0
	// when the mount after the create fails (zfs_main.c ignores
	// zfs_mount_and_share), and a kill after the create ioctl skips the
	// mount entirely; committing then hands out a mountpoint that is a
	// plain directory of the parent.""",
"""	// RequireMounted: the endpoint can mount (it runs as root), so a
	// filesystem volume zfs would mount is committed only once it is
	// mounted: the resume after the create's scan mounts an unmounted one
	// (OpMount) right before the commit. OpenZFS 2.4's `zfs create` and
	// `zfs clone` exit 0 when the mount after the create fails (zfs_main.c
	// ignores zfs_mount_and_share), and a kill after the create ioctl
	// skips the mount entirely; committing then would hand out a
	// mountpoint that is a plain directory of the parent."""),
])
edit('internal/endpoint/endpoint.go', [
("""	// It also says the endpoint can mount: CreateVolume then commits a
	// filesystem volume zfs would mount only once it is mounted, and is
	// Unavailable until then (Planner.RequireMounted). Without root
	// (-allow-non-root) nothing can mount, and volumes commit unmounted.""",
"""	// It also says the endpoint can mount: CreateVolume then commits a
	// filesystem volume zfs would mount only once it is mounted, and
	// mounts one the create left unmounted (`zfs mount`) before the commit
	// (Planner.RequireMounted). Without root (-allow-non-root) nothing can
	// mount, and volumes commit unmounted."""),
])
