p='internal/endpoint/seams_test.go'
s=open(p).read()
def rep(a,b):
    global s
    assert s.count(a)==1,a
    s=s.replace(a,b)
rep("""// S6: OpenZFS 2.4's create and clone exit 0 when the mount fails, and a
// kill after the create ioctl skips the mount. With RequireMounted (root),
// a mountable filesystem is committed only once a scan shows it mounted;
// otherwise CreateVolume is Unavailable and the row stays uncommitted.""",
"""// S6: OpenZFS 2.4's create and clone exit 0 when the mount fails, and a
// kill after the create ioctl skips the mount. With RequireMounted (root),
// a mountable filesystem is committed only once it is mounted: a resume
// that finds it unmounted mounts it (`zfs mount`) before the commit, so
// the commit runs only after a mount that succeeded.""")
rep("""		commit bool // else Unavailable
	}{
		{"unmounted, canmount=on", mp, unmounted(nil), false},""",
"""		commit bool // else mount, then commit
	}{
		{"unmounted, canmount=on", mp, unmounted(nil), false},""")
rep("""			if CodeOf(err) != Unavailable || len(plan) != 0 {
				t.Fatalf("plan %+v %v, want Unavailable", plan, err)
			}""",
"""			if err != nil || len(plan) != 2 || plan[0].Kind != OpMount || plan[0].Target != "tank/v/u" ||
				!plan[0].OkExists || !c.p.commits(plan[1:]) {
				t.Fatalf("plan %+v %v, want mount then commit", plan, err)
			}""")
rep("""// S6 end to end: the unmounted filesystem is never committed, and a retry
// stays Unavailable without writing anything until it is mounted.
func TestCreateUnmountedFilesystemIsUnavailable(t *testing.T) {""",
"""// S6 end to end: a filesystem the create left unmounted is mounted, then
// committed, within the same CreateVolume. When the mount fails, nothing
// is committed and the failure is retryable; the retry converges.
func TestCreateUnmountedFilesystemMountsThenCommits(t *testing.T) {""")
rep("""	for i := range 2 {
		n := len(sim.Calls)
		_, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "u", CapacityBytes: 1 << 30})
		if CodeOf(err) != Unavailable {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if got := sim.Prop(rParent+"/u", rNames.success()); got == "true" {
			t.Fatalf("attempt %d committed an unmounted filesystem", i)
		}
		if i > 0 {
			for _, c := range sim.Calls[n:] {
				if c[1] != "get" {
					t.Fatalf("retry mutated: %v", c)
				}
			}
		}
	}
	// mounted (here by a sharenfs change, as libzfs does): the retry commits
	if err := zfs.New(sim).Set(ctx, rParent+"/u", map[string]string{"sharenfs": "on"}); err != nil {
		t.Fatal(err)
	}
	if v, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "u", CapacityBytes: 1 << 30}); err != nil || v.Mountpoint == "" {
		t.Fatalf("after mount: %+v %v", v, err)
	}
""",
"""	mounted := func(ds string) string {
		d, err := zfs.New(sim).Get(ctx, ds, "mounted")
		if err != nil {
			t.Fatal(err)
		}
		return d.Props["mounted"].Value
	}
	v, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "u", CapacityBytes: 1 << 30})
	if err != nil || v.Mountpoint == "" || mounted(rParent+"/u") != "yes" || sim.Prop(rParent+"/u", rNames.success()) != "true" {
		t.Fatalf("create: %+v %v, mounted=%s", v, err, mounted(rParent+"/u"))
	}
	// the mount itself fails (a busy or non-empty mountpoint): nothing is
	// committed, the code is retryable, and the retry converges
	sim.Fail = func(args []string) error {
		if args[0] == "mount" {
			return &zfs.ExitError{Code: 1, Stderr: "cannot mount '/tank/x': directory is not empty\\n"}
		}
		return nil
	}
	_, err = ep.CreateVolume(ctx, CreateVolumeRequest{Name: "w", CapacityBytes: 1 << 30})
	if c := CodeOf(err); c != Aborted && c != Unavailable {
		t.Fatalf("mount failure: %v, want a retryable code", err)
	}
	if got := sim.Prop(rParent+"/w", rNames.success()); got == "true" || mounted(rParent+"/w") != "no" {
		t.Fatalf("committed (%q) or mounted an unmountable filesystem", got)
	}
	sim.Fail = nil
	if v, err := ep.CreateVolume(ctx, CreateVolumeRequest{Name: "w", CapacityBytes: 1 << 30}); err != nil || mounted(rParent+"/w") != "yes" {
		t.Fatalf("retry: %+v %v", v, err)
	}
""")
open(p,'w').write(s)
