import sys
p=sys.argv[1]
s=open(p).read()
def rep(old,new,count=1):
    global s
    assert s.count(old)==count, (s.count(old), old)
    s=s.replace(old,new)
rep('''func (f *faultRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	crash := false
	if mutating(args) {
		crash = f.n == f.crashAt
		f.crashed = f.crashed || crash
		f.n++
	}
	f.mu.Unlock()
	lost := &zfs.ExitError{Args: args, Code: -1, Underlay: errCrash}''','''// step counts one mutation and says whether it is the one to crash at.
func (f *faultRunner) step() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	crash := f.n == f.crashAt
	f.crashed = f.crashed || crash
	f.n++
	return crash
}

// faultRoot puts the endpoint's root owner and mode change (not a zfs
// call) into the same count: it is a mutation a crash can precede or
// follow like any other.
type faultRoot struct {
	endpoint.RootSetter
	f *faultRunner
}

func (r faultRoot) SetRoot(ctx context.Context, dataset, mountpoint string, uid, gid, mode int64) error {
	crash := r.f.step()
	if crash && !r.f.after {
		return errCrash
	}
	err := r.RootSetter.SetRoot(ctx, dataset, mountpoint, uid, gid, mode)
	if crash {
		return errCrash
	}
	return err
}

func (f *faultRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	crash := mutating(args) && f.step()
	lost := &zfs.ExitError{Args: args, Code: -1, Underlay: errCrash}''')
rep('''	{name: "publish", nfs: true,''','''	// N11: the root's owner and mode are set before the commit, by the
	// resume, on every pass: a crash on either side of the change converges
	{name: "create-nfs-root", nfs: true, op: createOp(endpoint.CreateVolumeRequest{Name: "v", CapacityBytes: 16 * cmib,
		NFS: &endpoint.NFSExport{Clients: []string{"10.20.0.0/24"}, UID: ptr(uint32(1000)), GID: ptr(uint32(2000)), Mode: ptr(uint32(0o2775))}})},
	{name: "restore-nfs-root-mode", nfs: true,
		setup: then(mk(endpoint.CreateVolumeRequest{Name: "src", NFS: &endpoint.NFSExport{Clients: []string{"10.20.0.0/24"}, UID: ptr(uint32(1000))}}),
			snapOf("s", "src")),
		op: createOp(endpoint.CreateVolumeRequest{Name: "r", SourceSnapshotID: "src@s",
			NFS: &endpoint.NFSExport{Clients: []string{"10.20.0.0/24"}, Mode: ptr(uint32(0o700))}})},
	{name: "publish", nfs: true,''')
rep('''func fingerprint(t *testing.T, h *harness) string {
	t.Helper()
	p := endpoint.DefaultPropertyPrefix + ":"
	props := []string{"type", "volsize", "refquota", "refreservation", "origin", "sharenfs", "compression", "recordsize",''','''func fingerprint(t *testing.T, h *harness) string {
	t.Helper()
	p := endpoint.DefaultPropertyPrefix + ":"
	props := []string{"type", "volsize", "refquota", "refreservation", "origin", "sharenfs", "compression", "recordsize", "mountpoint", "mounted",''')
rep('''		lines = append(lines, strings.TrimPrefix(d.Name, h.Parent+"/")+" "+strings.Join(kv, " "))''','''		if d.Props["mounted"].Value == "yes" && d.Name != h.Parent {
			// the root directory's owner and mode (N11)
			r := h.rootOf(t, d.Name, d.Props["mountpoint"].Value)
			kv = append(kv, fmt.Sprintf("root=%d:%d:%04o", r.UID, r.GID, r.Mode))
		}
		lines = append(lines, strings.TrimPrefix(d.Name, h.Parent+"/")+" "+strings.Join(kv, " "))''')
rep('''func crashEndpoint(t *testing.T, h *harness, r zfs.Runner, late bool) *endpoint.Endpoint {
	t.Helper()
	cfg := endpoint.Config{Parent: h.Parent, LockWait: time.Second, FileExport: true}
	if h.sim != nil {
		cfg.Etab = h.sim
	}
	if late {''','''func crashEndpoint(t *testing.T, h *harness, r zfs.Runner, late bool) *endpoint.Endpoint {
	t.Helper()
	cfg := endpoint.Config{Parent: h.Parent, LockWait: time.Second, FileExport: true, NFSServers: []string{"192.0.2.10"}}
	cfg.Root = endpoint.RootSetter(endpoint.LocalRoot{})
	if h.sim != nil {
		cfg.Etab, cfg.Root = h.sim, h.sim
	}
	if f, ok := r.(*faultRunner); ok {
		cfg.Root = faultRoot{cfg.Root, f}
	}
	if late {''')
open(p,'w').write(s)
