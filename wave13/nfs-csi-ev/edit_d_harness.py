import sys
root = sys.argv[1]
p = root + '/internal/endpoint/endpoint_test.go'
s = open(p).read()


def sub(old, new):
    global s
    if old not in s:
        raise SystemExit('missing: ' + old[:80])
    s = s.replace(old, new, 1)


sub('''func setupPrefix(t *testing.T, prefix string) (*endpoint.Endpoint, *harness) {
	t.Helper()
''', '''func setupPrefix(t *testing.T, prefix string) (*endpoint.Endpoint, *harness) {
	t.Helper()
	return setupConfig(t, prefix, nil)
}

// setupNFS is setup for an endpoint advertising NFS servers.
func setupNFS(t *testing.T, servers ...string) (*endpoint.Endpoint, *harness) {
	t.Helper()
	return setupConfig(t, "", func(c *endpoint.Config) { c.NFSServers = servers })
}

// setupConfig is setupPrefix with mod applied to the endpoint's Config.
func setupConfig(t *testing.T, prefix string, mod func(*endpoint.Config)) (*endpoint.Endpoint, *harness) {
	t.Helper()
''')
sub('''	if err := h.Z.CreateFilesystem(ctx, h.Parent, nil, true); err != nil {
		t.Fatal(err)
	}
	cfg := endpoint.Config{Parent: h.Parent, LockWait: time.Second, FileExport: true, PropertyPrefix: prefix}
''', '''	if err := h.Z.CreateFilesystem(ctx, h.Parent, nil, true); err != nil {
		t.Fatal(err)
	}
	return h.endpoint(t, prefix, mod), h
}

// endpoint starts another endpoint on h's parent, as a restart does.
func (h *harness) endpoint(t *testing.T, prefix string, mod func(*endpoint.Config)) *endpoint.Endpoint {
	t.Helper()
	cfg := endpoint.Config{Parent: h.Parent, LockWait: time.Second, FileExport: true, PropertyPrefix: prefix}
''')
sub('''	ep, err := endpoint.New(h.Z, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ep, h
}''', '''	if mod != nil {
		mod(&cfg)
	}
	ep, err := endpoint.New(h.Z, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return ep
}''')
open(p, 'w').write(s)
