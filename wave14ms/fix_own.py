p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/ownership_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert a in s, a
    s = s.replace(a, b)


rep('''		"democratic-csi:managed_resource": "true", "democratic-csi:csi_volume_name": "half",''',
    '''		mark("managed_resource"): "true", mark("csi_volume_name"): "half",''')
rep('''		"democratic-csi:managed_resource": "true", "democratic-csi:provision_success": "true",
		"democratic-csi:csi_volume_name": "orig", "refquota": "1048576",''',
    '''		mark("managed_resource"): "true", mark("provision_success"): "true",
		mark("csi_volume_name"): "orig", "refquota": "1048576",''')
rep('''		"democratic-csi:managed_resource": "true", "democratic-csi:provision_success": "true",
		"democratic-csi:csi_volume_name": "orig",
	}''', '''		mark("managed_resource"): "true", mark("provision_success"): "true",
		mark("csi_volume_name"): "orig",
	}''')
rep('sim.Receive(cp, "democratic-csi:"+k, v)', 'sim.Receive(cp, mark(k), v)')
rep('sim.Receive(cp+"@snapA", "democratic-csi:"+k, v)', 'sim.Receive(cp+"@snapA", mark(k), v)')
rep('''// ADR 0003, "never touch''', '''// mark names a marker under the prefix setup's endpoint reads. A marker
// under any other prefix is an ordinary user property, so a fixture built
// with one tests nothing about ownership (wave12 AN-02): every row would be
// unowned for the trivial reason. The tests below also pin a positive
// control, so a prefix drift fails instead of passing vacuously.
func mark(k string) string { return endpoint.DefaultPropertyPrefix + ":" + k }

// ADR 0003, "never touch''')
open(p, 'w').write(s)
