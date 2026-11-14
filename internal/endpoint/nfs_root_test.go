package endpoint

import (
	"strings"
	"testing"
)

// N11 (wave9; csi-nfs-design §13 Q1(a)): an NFS export's root owner and
// mode. Pure planner tests: rows in, plan or error out.

func u32(n uint32) *uint32 { return &n }

func rootReq(name string, uid, gid, mode *uint32) CreateVolumeRequest {
	return nfsReq(name, &NFSExport{Clients: []string{"10.20.0.0/24"}, UID: uid, GID: gid, Mode: mode})
}

// uncommitted is an NFS volume a crashed or first-pass create left: its
// export and server set, not committed.
func uncommitted(id string, mut func(*Row)) Row {
	r := nfsVol(id, "rw=10.20.0.0/24", srv1)
	r.Committed = false
	if mut != nil {
		mut(&r)
	}
	return r
}

func TestValidateNFSRoot(t *testing.T) {
	t.Parallel()
	for _, ok := range []CreateVolumeRequest{
		rootReq("v", u32(0), u32(0), u32(0)),
		rootReq("v", u32(MaxRootID), u32(MaxRootID), u32(0o7777)),
		rootReq("v", nil, nil, u32(0o2775)),
		rootReq("v", u32(1000), nil, nil),
	} {
		if _, err := nfsPl.validateNFS(ok); err != nil {
			t.Errorf("%+v: %v", ok.NFS, err)
		}
	}
	for _, bad := range []CreateVolumeRequest{
		rootReq("v", u32(MaxRootID+1), nil, nil),
		rootReq("v", u32(1<<32-1), nil, nil), // chown(2)'s "unchanged"
		rootReq("v", nil, u32(MaxRootID+1), nil),
		rootReq("v", nil, nil, u32(0o10000)), // not a permission bit
		rootReq("v", nil, nil, u32(0o40755)), // S_IFDIR: a type, not a mode
	} {
		if _, err := nfsPl.validateNFS(bad); CodeOf(err) != InvalidArgument {
			t.Errorf("%+v: %v, want InvalidArgument", bad.NFS, err)
		}
	}
}

func TestPlanCreateVolumeNFSRoot(t *testing.T) {
	t.Parallel()
	noMount := nfsPl
	noMount.RequireMounted = false
	cases := []struct {
		name string
		pl   *Planner
		rows []Row
		req  CreateVolumeRequest
		want string // the plan's text, or the error's code
		code Code
	}{
		// the create never commits: the resume sets the root on the
		// mountpoint its scan read, then commits
		{name: "create", req: rootReq("a", u32(1000), u32(2000), u32(0o2775)),
			want: "create-filesystem tank/v/a"},
		{name: "create, without mounting rights, still leaves the commit to the resume", pl: &noMount,
			req: rootReq("a", u32(1000), nil, nil), want: "create-filesystem tank/v/a"},
		{name: "resume sets the root, then commits", rows: []Row{uncommitted("a", nil)},
			req:  rootReq("a", u32(1000), u32(2000), u32(0o2775)),
			want: "set-root tank/v/a path=/tank/v/a gid=2000 mode=2775 uid=1000\nset tank/v/a democratic-csi:provision_success=true"},
		{name: "only what is asked", rows: []Row{uncommitted("a", nil)},
			req:  rootReq("a", nil, nil, u32(0o777)),
			want: "set-root tank/v/a path=/tank/v/a mode=0777\nset tank/v/a democratic-csi:provision_success=true"},
		{name: "unmounted: mount, then the root, then commit", rows: []Row{uncommitted("a", func(r *Row) { r.Mounted = false })},
			req:  rootReq("a", u32(0), u32(0), nil),
			want: "mount tank/v/a +ok-exists\nset-root tank/v/a path=/tank/v/a gid=0 uid=0\nset tank/v/a democratic-csi:provision_success=true"},
		{name: "unmounted, and the endpoint cannot mount", pl: &noMount, rows: []Row{uncommitted("a", func(r *Row) { r.Mounted = false })},
			req: rootReq("a", u32(1000), nil, nil), code: FailedPrecondition},
		{name: "no mountpoint", rows: []Row{uncommitted("a", func(r *Row) { r.Mount, r.Mounted = "none", false })},
			req: rootReq("a", u32(1000), nil, nil), code: FailedPrecondition},
		{name: "canmount off", rows: []Row{uncommitted("a", func(r *Row) { r.CanMount, r.Mounted = "off", false })},
			req: rootReq("a", u32(1000), nil, nil), code: FailedPrecondition},
		// file metadata, not volume state: a tenant may chmod its root, and
		// a repeat must not turn that into AlreadyExists
		{name: "committed: not compared", rows: []Row{nfsVol("a", "rw=10.20.0.0/24", srv1)},
			req: rootReq("a", u32(1000), u32(2000), u32(0o700))},
		{name: "no root asked: no set-root", rows: []Row{uncommitted("a", nil)},
			req:  rootReq("a", nil, nil, nil),
			want: "set tank/v/a democratic-csi:provision_success=true"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := nfsPl
			if tc.pl != nil {
				p = *tc.pl
			}
			plan, err := p.PlanCreateVolume(NewTable(tc.rows), tc.req)
			if CodeOf(err) != tc.code {
				t.Fatalf("err %v, want code %d", err, tc.code)
			}
			if tc.code != OK {
				if plan != nil {
					t.Fatalf("refused, but planned %v", plan)
				}
				return
			}
			// the create's own -o are other tests' business: compare kinds
			// and targets for it, whole ops for the rest
			var got []string
			for _, op := range plan {
				line := strings.TrimSuffix(Plan{op}.String(), "\n")
				if op.Kind == OpCreateFS {
					line = op.Kind.String() + " " + op.Target
				}
				got = append(got, line)
			}
			if g := strings.Join(got, "\n"); g != tc.want {
				t.Fatalf("plan\n%s\nwant\n%s", g, tc.want)
			}
		})
	}
}

func TestRootPropsRoundTrip(t *testing.T) {
	t.Parallel()
	for _, a := range []rootAttrs{noRoot, {0, 0, 0}, {1000, -1, 0o2775}, {-1, MaxRootID, -1}, {-1, -1, 0o7777}} {
		got, err := rootOf(a.props())
		if err != nil || got != a {
			t.Errorf("%+v: %+v, %v", a, got, err)
		}
	}
	for _, bad := range []map[string]string{{"uid": "-1"}, {"mode": "10000"}, {"mode": "9"}, {"owner": "0"}, {"gid": "2147483648"}} {
		if _, err := rootOf(bad); err == nil {
			t.Errorf("%v: accepted", bad)
		}
	}
}

func TestParseMountinfo(t *testing.T) {
	t.Parallel()
	in := "21 1 0:20 / / rw - zfs rpool/ROOT rw\n" +
		"40 21 0:52 / /tank/k8s\\040vols/a rw,nosuid,nodev,noexec shared:9 - zfs tank/k8s\\040vols/a rw,xattr\n" +
		"41 21 0:53 /sub /tank/k8s\\040vols/a rw master:3 shared:4 - zfs tank/other rw\n" +
		"garbage\n" +
		"42 21 x:1 / /x rw - zfs tank/x rw\n"
	es := parseMountinfo([]byte(in))
	if len(es) != 3 {
		t.Fatalf("%d entries: %+v", len(es), es)
	}
	e := es[1]
	if e.major != 0 || e.minor != 52 || e.root != "/" || e.point != "/tank/k8s vols/a" || e.fstype != "zfs" || e.source != "tank/k8s vols/a" {
		t.Fatalf("%+v", e)
	}
	// the last mount at a point is the one on top
	if top, ok := mountedAt(es, "/tank/k8s vols/a"); !ok || top.source != "tank/other" || top.root != "/sub" {
		t.Fatalf("on top: %+v %v", top, ok)
	}
	if _, ok := mountedAt(es, "/nowhere"); ok {
		t.Fatal("found a mount at /nowhere")
	}
	if got := unescapeMount(`a\134b\011c\012\`); got != "a\\b\tc\n\\" {
		t.Fatalf("unescape: %q", got)
	}
}
