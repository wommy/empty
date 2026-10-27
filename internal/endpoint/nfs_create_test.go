package endpoint

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// Static NFS export at create (csi-nfs-design §3-§6, wave13 Q1/Q2): pure
// planner tests, rows in, plan or error out.

const (
	srv1      = "192.0.2.10"
	srv2      = "nfs.example.com"
	serverKey = "democratic-csi:nfs_server"
)

// nfsPl is pl as an endpoint running as root that advertises srv1, srv2.
var nfsPl = func() Planner {
	p := pl
	p.RequireMounted, p.NFSServers, p.NFSMinPrefix = true, []string{srv1, srv2}, DefaultNFSMinPrefix
	return p
}()

func nfsReq(name string, nfs *NFSExport) CreateVolumeRequest {
	return CreateVolumeRequest{Name: name, Kind: Filesystem, CapacityBytes: 1 << 30, NFS: nfs}
}

// nfsVol is a committed 1 GiB filesystem volume exported with share and
// created with server (none when "").
func nfsVol(id, share, server string) Row {
	return fsv(id, func(r *Row) {
		r.Refquota, r.ShareNFS, r.NFSServer = 1<<30, share, server
	})
}

func TestValidateNFSExport(t *testing.T) {
	t.Parallel()
	good := map[string]string{
		"10.20.0.0/24":              "rw=10.20.0.0/24",
		"10.20.1.0/24,10.20.0.0/24": "rw=10.20.0.0/24:10.20.1.0/24", // sorted, never as given
		"10.20.0.5/32":              "rw=10.20.0.5/32",
		"10.20.0.0/16":              "rw=10.20.0.0/16",
	}
	for in, want := range good {
		w, err := nfsPl.validateNFS(nfsReq("v", &NFSExport{Clients: strings.Split(in, ",")}))
		if err != nil || ShareNFS(w.clients) != want || w.server != srv1 || w.explicit {
			t.Errorf("%q: %+v, %v; want %s on %s", in, w, err, want, srv1)
		}
	}
	// refused for the value, never masked, completed or reordered into
	// something valid; the message names the value
	for _, bad := range [][]string{
		nil, {""}, {"10.20.0.0/24", ""},
		{"10.20.0.5"},                 // no /N: nfs-utils resolves a bare address
		{"10.20.0.5/24"},              // host bits
		{"10.0.0.0/8"}, {"0.0.0.0/0"}, // below the floor; everyone
		{"10.20.0.0/24", "10.20.0.128/25"}, // overlap
		{"10.20.0.0/24", "10.20.0.0/24"},   // duplicate
		{"host.example.com/32"}, {"*"},     // names, wildcards
		{"2001:db8::/64"},                         // IPv6
		{"10.20.0.0/24:10.20.1.0/24"},             // sharenfs' separator
		{"10.20.0.0/24", "no_root_squash"},        // an option
		{"010.020.000.000/24"}, {"10.20.0.0/024"}, // inet_aton spellings
	} {
		_, err := nfsPl.validateNFS(nfsReq("v", &NFSExport{Clients: bad}))
		if CodeOf(err) != InvalidArgument {
			t.Errorf("%q: %v, want InvalidArgument", bad, err)
		}
	}
	if _, err := nfsPl.validateNFS(nfsReq("v", &NFSExport{Clients: []string{"10.20.0.0/24"}, Server: "198.51.100.1"})); CodeOf(err) != InvalidArgument || !strings.Contains(err.Error(), "198.51.100.1") {
		t.Errorf("unadvertised server: %v", err)
	}
	r := nfsReq("v", &NFSExport{Clients: []string{"10.20.0.0/24"}})
	r.Kind = Block
	if _, err := nfsPl.validateNFS(r); CodeOf(err) != InvalidArgument {
		t.Errorf("block: %v", err)
	}
	if _, err := pl.validateNFS(nfsReq("v", &NFSExport{Clients: []string{"10.20.0.0/24"}})); CodeOf(err) != FailedPrecondition {
		t.Errorf("no advertised server: %v", err)
	}
}

func TestPlanCreateVolumeNFS(t *testing.T) {
	t.Parallel()
	exp := func(server string, ro bool, clients ...string) *NFSExport {
		return &NFSExport{Clients: clients, ReadOnly: ro, Server: server}
	}
	reordered := nfsPl
	reordered.NFSServers = []string{srv2, srv1} // the endpoint restarted with -nfs-server reordered
	snapSrc := func(r *Row) { r.Refquota = 1 << 30 }
	cases := []struct {
		name string
		pl   Planner
		rows []Row
		req  CreateVolumeRequest
		// props: what the create (or clone) op sets, among sharenfs and
		// the server; set: the resume's convergence
		props, set map[string]string
		code       Code
	}{
		{name: "create records the default server with the export", rows: nil,
			req:   nfsReq("a", exp("", false, "10.20.1.0/24", "10.20.0.0/24")),
			props: map[string]string{"sharenfs": "rw=10.20.0.0/24:10.20.1.0/24", serverKey: srv1}},
		{name: "create records an explicit server", rows: nil,
			req:   nfsReq("a", exp(srv2, true, "10.20.0.0/24")),
			props: map[string]string{"sharenfs": "ro=10.20.0.0/24", serverKey: srv2}},
		// N13: a restore takes the request's export, never the source's
		{name: "restore takes the request's export", rows: []Row{nfsVol("src", "rw=10.9.0.0/16", srv2), snap("src@s", snapSrc)},
			req: func() CreateVolumeRequest {
				r := nfsReq("a", exp("", false, "10.30.0.0/24"))
				r.SourceSnapshotID = "src@s"
				return r
			}(),
			props: map[string]string{"sharenfs": "rw=10.30.0.0/24", serverKey: srv1}},
		// uncommitted: nobody holds its context, so it converges, server
		// included (wave13 Q1 3)
		{name: "uncommitted converges export and server", rows: []Row{func() Row {
			r := nfsVol("a", "rw=10.9.0.0/16", srv2)
			r.Committed, r.Mounted = false, true
			return r
		}()}, req: nfsReq("a", exp("", false, "10.20.0.0/24")),
			set: map[string]string{"sharenfs": "rw=10.20.0.0/24", serverKey: srv1}},
		// N9 (inverted last assertion, wave13 Q1): committed
		{name: "committed, same set in another order", rows: []Row{nfsVol("a", "rw=10.20.0.0/24:10.20.1.0/24", srv1)},
			req: nfsReq("a", exp("", false, "10.20.1.0/24", "10.20.0.0/24"))},
		{name: "committed, server omitted after a reorder: the recorded one", pl: reordered, rows: []Row{nfsVol("a", "rw=10.20.0.0/24", srv1)},
			req: nfsReq("a", exp("", false, "10.20.0.0/24"))},
		{name: "committed, the recorded server named", rows: []Row{nfsVol("a", "rw=10.20.0.0/24", srv2)},
			req: nfsReq("a", exp(srv2, false, "10.20.0.0/24"))},
		{name: "committed, another server named", rows: []Row{nfsVol("a", "rw=10.20.0.0/24", srv1)},
			req: nfsReq("a", exp(srv2, false, "10.20.0.0/24")), code: AlreadyExists},
		{name: "committed, other clients", rows: []Row{nfsVol("a", "rw=10.20.0.0/24", srv1)},
			req: nfsReq("a", exp("", false, "10.20.2.0/24")), code: AlreadyExists},
		{name: "committed, other mode", rows: []Row{nfsVol("a", "rw=10.20.0.0/24", srv1)},
			req: nfsReq("a", exp("", true, "10.20.0.0/24")), code: AlreadyExists},
		{name: "committed, foreign sharenfs", rows: []Row{nfsVol("a", "rw=10.20.0.0/24,no_root_squash", srv1)},
			req: nfsReq("a", exp("", false, "10.20.0.0/24")), code: AlreadyExists},
		{name: "committed, inherited sharenfs", rows: []Row{func() Row {
			r := nfsVol("a", "off", srv1)
			r.ShareNFSLocal = false
			return r
		}()}, req: nfsReq("a", exp("", false, "10.20.0.0/24")), code: AlreadyExists},
		{name: "committed without NFS, now asked for it", rows: []Row{nfsVol("a", "off", "")},
			req: nfsReq("a", exp("", false, "10.20.0.0/24")), code: AlreadyExists},
		// legacy (created before the server was recorded): S1 keeps it
		// without a server; naming one is a different volume
		{name: "committed legacy export, server omitted", rows: []Row{nfsVol("a", "rw=10.20.0.0/24", "")},
			req: nfsReq("a", exp("", false, "10.20.0.0/24"))},
		{name: "committed legacy export, server named", rows: []Row{nfsVol("a", "rw=10.20.0.0/24", "")},
			req: nfsReq("a", exp(srv1, false, "10.20.0.0/24")), code: AlreadyExists},
		// without an export the request says nothing about one: a volume a
		// front end later published stays compatible
		{name: "committed export, request without one", rows: []Row{nfsVol("a", "rw=10.20.0.0/24", srv1)},
			req: nfsReq("a", nil)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.pl
			if p.Parent == "" {
				p = nfsPl
			}
			plan, err := p.PlanCreateVolume(NewTable(slices.Clone(tc.rows)), tc.req)
			if CodeOf(err) != tc.code {
				t.Fatalf("err %v, want code %d", err, tc.code)
			}
			if tc.code != OK {
				if plan != nil {
					t.Fatalf("refused, but planned %v", plan)
				}
				return
			}
			pick := func(m map[string]string) map[string]string {
				out := map[string]string{}
				for _, k := range []string{"sharenfs", serverKey} {
					if v, ok := m[k]; ok {
						out[k] = v
					}
				}
				return out
			}
			var created, set map[string]string
			for _, op := range plan {
				switch op.Kind {
				case OpCreateFS, OpClone:
					created = pick(op.Props)
				case OpSet:
					if s := pick(op.Props); len(s) > 0 {
						set = s
					}
				}
			}
			if !maps.Equal(created, tc.props) || !maps.Equal(set, tc.set) {
				t.Fatalf("plan %v\ncreated %v, want %v\nset %v, want %v", plan, created, tc.props, set, tc.set)
			}
			if len(tc.props) > 0 && p.commits(plan) {
				t.Fatalf("an NFS create commits before the resume verifies it: %v", plan)
			}
		})
	}
}

// Q1/N4/N16: what a volume reports. The server is the recorded local one
// only; the export is the local canonical sharenfs.
func TestVolumeViewNFS(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		row  Row
		want *NFSExport
	}{
		{"exported at create", nfsVol("a", "rw=10.20.0.0/24:10.20.1.0/24", srv1),
			&NFSExport{Server: srv1, Clients: []string{"10.20.0.0/24", "10.20.1.0/24"}}},
		{"read-only", nfsVol("a", "ro=10.20.0.0/24", srv2), &NFSExport{Server: srv2, Clients: []string{"10.20.0.0/24"}, ReadOnly: true}},
		{"mixed modes are not read-only", nfsVol("a", "rw=10.20.0.0/24,ro=10.21.0.0/24", srv1),
			&NFSExport{Server: srv1, Clients: []string{"10.20.0.0/24", "10.21.0.0/24"}}},
		{"recorded server, export now off", nfsVol("a", "off", srv1), &NFSExport{Server: srv1}},
		{"published without a server", nfsVol("a", "rw=10.20.0.5/32", ""), &NFSExport{Clients: []string{"10.20.0.5/32"}}},
		{"plain volume", nfsVol("a", "off", ""), nil},
		{"foreign sharenfs is no export of ours", nfsVol("a", "on", ""), nil},
		// N16: an inherited or received server is not this volume's
		{"server set elsewhere", func() Row {
			r := nfsVol("a", "off", "")
			r.NFSServerElsewhere = true
			return r
		}(), nil},
	}
	for _, tc := range cases {
		if got := volumeView(tc.row).NFS; !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %+v, want %+v", tc.name, got, tc.want)
		}
	}
}

// N16 and wave13 Q1 5: report-only drift on the recorded server.
func TestPlanReconcileNFSServer(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		row   Row
		skips []string
	}{
		{"advertised", nfsVol("a", "rw=10.20.0.0/24", srv2), nil},
		{"no longer advertised", nfsVol("a", "rw=10.20.0.0/24", "198.51.100.1"), []string{"a:" + SkipNFSServerUnadvertised}},
		{"inherited or received", func() Row {
			r := nfsVol("a", "rw=10.20.0.0/24", "")
			r.NFSServerElsewhere = true
			return r
		}(), []string{"a:" + SkipNFSServerNotLocal}},
		{"uncommitted is not reported", func() Row {
			r := nfsVol("a", "rw=10.20.0.0/24", "198.51.100.1")
			r.Committed, r.Creation = false, recNow
			return r
		}(), nil}, // desired and in flight: its server may still converge
	}
	for _, tc := range cases {
		plan, err := nfsPl.PlanReconcile(table(tc.row), desired("a"))
		if err != nil {
			t.Fatal(err)
		}
		if _, skips := summary(plan); !slices.Equal(skips, tc.skips) {
			t.Errorf("%s: skipped %q, want %q", tc.name, skips, tc.skips)
		}
	}
}
