p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/ownership_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


# helper: the anchor control
rep('''func mark(k string) string { return endpoint.DefaultPropertyPrefix + ":" + k }
''', '''func mark(k string) string { return endpoint.DefaultPropertyPrefix + ":" + k }

// markIsLive fails the test unless vol, a volume the endpoint created,
// carries its name under mark(): the fixtures' markers are the ones read.
func markIsLive(t *testing.T, h *harness, vol string) {
	t.Helper()
	if got := h.Prop(h.Parent+"/"+vol, mark("csi_volume_name")); got != vol {
		t.Fatalf("%s: %s=%q: the fixtures' marker prefix is not the endpoint's", vol, mark("csi_volume_name"), got)
	}
}
''')

# 1. clone sources: positive control on half once committed
rep('''	for _, s := range []string{"foreign@volume-source-for-volume-c1", "half@volume-source-for-volume-c5"} {
		if h.Has(h.Parent + "/" + s) {
			t.Errorf("snapshot %s cut on a dataset the endpoint does not own", s)
		}
	}
}''', '''	for _, s := range []string{"foreign@volume-source-for-volume-c1", "half@volume-source-for-volume-c5"} {
		if h.Has(h.Parent + "/" + s) {
			t.Errorf("snapshot %s cut on a dataset the endpoint does not own", s)
		}
	}
	// control: committed, the same hand-made volume is a source, so the
	// refusal above was for "uncommitted", not for unreadable markers
	markIsLive(t, h, "v")
	if err := h.Z.Set(ctx, h.Parent+"/half", map[string]string{mark("provision_success"): "true"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "c6", SourceVolumeID: "half"}); err != nil {
		t.Errorf("c6 from committed half: %v", err)
	}
}''')

# 2. name must match: positive control
rep('''	if !h.Has(h.Parent+"/moved") || h.Prop(h.Parent+"/moved", "refquota") != "1048576" {
		t.Error("dataset touched")
	}
}''', '''	if !h.Has(h.Parent+"/moved") || h.Prop(h.Parent+"/moved", "refquota") != "1048576" {
		t.Error("dataset touched")
	}
	// control: the same markers naming the dataset's own id are a volume,
	// so the refusals above were for the name, not for unreadable markers
	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/same", map[string]string{
		mark("managed_resource"): "true", mark("provision_success"): "true",
		mark("csi_volume_name"): "same", "refquota": "1048576",
	}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.GetVolume(ctx, "same"); err != nil {
		t.Errorf("GetVolume(same), the control: %v", err)
	}
}''')

# 3. received copy: a restore received under its own name
rep('''	for k, v := range map[string]string{"managed_resource": "true", "csi_snapshot_name": "snapA", "csi_snapshot_source_volume_id": "pvc-1"} {
		sim.Receive(cp+"@snapA", mark(k), v)
	}
''', '''	for k, v := range map[string]string{"managed_resource": "true", "csi_snapshot_name": "snapA", "csi_snapshot_source_volume_id": "pvc-1"} {
		sim.Receive(cp+"@snapA", mark(k), v)
	}
	// a backup restored under its original name: the received markers name
	// the dataset's own id, so only "received is not local" stands between
	// it and adoption
	rs := h.Parent + "/pvc-2"
	if err := h.Z.CreateFilesystem(ctx, rs, nil, false); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"managed_resource": "true", "provision_success": "true", "csi_volume_name": "pvc-2"} {
		sim.Receive(rs, mark(k), v)
	}
	markIsLive(t, h, "pvc-1")
''')
rep('''	if !h.Has(cp) || !h.Has(cp+"@snapA") {
		t.Error("received copy destroyed")
	}
}''', '''	if _, err := ep.GetVolume(ctx, "pvc-2"); endpoint.CodeOf(err) != endpoint.NotFound {
		t.Errorf("GetVolume restored copy: %v", err)
	}
	if err := ep.DeleteVolume(ctx, "pvc-2"); endpoint.CodeOf(err) != endpoint.FailedPrecondition {
		t.Errorf("DeleteVolume restored copy: %v", err)
	}
	if !h.Has(cp) || !h.Has(cp+"@snapA") || !h.Has(rs) {
		t.Error("received copy destroyed")
	}
}''')

# 4. reconcile: received under its own name is spared too
rep('''	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/moved", map[string]string{
		mark("managed_resource"): "true", mark("provision_success"): "true",
		mark("csi_volume_name"): "orig",
	}, false); err != nil {
		t.Fatal(err)
	}
	p, err''', '''	if err := h.Z.CreateFilesystem(ctx, h.Parent+"/moved", map[string]string{
		mark("managed_resource"): "true", mark("provision_success"): "true",
		mark("csi_volume_name"): "orig",
	}, false); err != nil {
		t.Fatal(err)
	}
	rs := h.Parent + "/restored"
	if err := h.Z.CreateFilesystem(ctx, rs, nil, false); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"managed_resource": "true", "provision_success": "true", "csi_volume_name": "restored"} {
		sim.Receive(rs, mark(k), v)
	}
	markIsLive(t, h, "keep")
	p, err''')
rep('''	if !h.Has(cp) || !h.Has(h.Parent+"/moved") {''', '''	if !h.Has(cp) || !h.Has(h.Parent+"/moved") || !h.Has(rs) {''')
open(p, 'w').write(s)
