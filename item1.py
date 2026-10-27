import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a641f1e792d2b8587/internal/endpoint')
p='cov24_test.go'
s=open(p).read()
old=s[s.index('// Item 7 (wave22): an NFS volume'):]
new='''// Item 7 (wave22): an NFS volume's root owner is set after its create, so
// a failed SetRoot leaves a half-made volume. An unclassified failure
// there must be retryable (Aborted), never Internal, which would stop the
// provisioner retrying and strand it; a retry with the setter fixed
// converges with the owner set. LocalRoot's own Internal (an open, stat,
// mount table or chown failure it does not classify) is unclassified too
// (wave25 item 1): it is what production returns, not a bare error.
func TestFileExportCreateRootFailureIsRetryable(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"bare error", errors.New("x")},
		{"LocalRoot's Internal", &endpoint.Error{Code: endpoint.Internal, Msg: "open /tank/k8s/vols/pvc", Err: syscall.EIO}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			var rootErr error
			sim, _, ep := simNFS(t, func(c *endpoint.Config) {
				c.Root = failRoot{c.Etab.(*zfstest.Sim), &rootErr}
			})
			uid := uint32(1000)
			req := nfsCreate("pvc", "", "10.20.0.0/24")
			req.NFS.UID = &uid
			rootErr = tc.err
			_, err := ep.CreateVolume(ctx, req)
			wantCode(t, err, endpoint.Aborted)
			if !errors.Is(err, rootErr) && !strings.Contains(err.Error(), rootErr.Error()) {
				t.Fatalf("the setter's error is not wrapped: %v", err)
			}
			const ds = "tank/k8s/vols/pvc"
			if !sim.Has(ds) || sim.Prop(ds, endpoint.DefaultPropertyPrefix+":provision_success") == "true" {
				t.Fatal("want the volume created and uncommitted")
			}
			rootErr = nil
			if _, err := ep.CreateVolume(ctx, req); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if r, ok := sim.RootOf(ds); !ok || r.UID != uid {
				t.Fatalf("root %+v after the retry, want uid %d", r, uid)
			}
		})
	}
}
'''
s=s.replace(old,new)
s=s.replace('\t"strings"\n\t"testing"\n','\t"strings"\n\t"syscall"\n\t"testing"\n',1)
open(p,'w').write(s)
p='cov24_internal_test.go'
s=open(p).read()
a='''		{"unclassified, after a change", plain, 1, plan, Aborted, "earlier ops applied"},
'''
assert a in s
s=s.replace(a,a+'''		// LocalRoot's own Internal is no classification: it is an
		// unclassified failure, as a bare error is
		{"Internal, first op", &Error{Code: Internal, Msg: "open"}, 0, root, Internal, "op 1/1 set-root tank/v/a"},
		{"Internal, after a change", &Error{Code: Internal, Msg: "open"}, 1, plan, Aborted, "earlier ops applied"},
''')
b='''		t.Errorf("unclassified, resumed: %v (code %d), want Aborted", got, CodeOf(got))
	}
'''
assert b in s
s=s.replace(b,b+'''	internal := &Error{Code: Internal, Msg: "open", Err: plain}
	if got := resumeErr(rootErr(internal, 0, root)); CodeOf(got) != Aborted || !errors.Is(got, plain) {
		t.Errorf("Internal, resumed: %v (code %d), want Aborted", got, CodeOf(got))
	}
''')
open(p,'w').write(s)
