import sys
root = sys.argv[1]

def sub(path, pairs):
    p = root + '/' + path
    s = open(p).read()
    for old, new in pairs:
        assert old in s, (path, old[:80])
        s = s.replace(old, new, 1)
    open(p, 'w').write(s)

sub('internal/endpoint/plan.go', [
('''		return deletion{}, errorf(FailedPrecondition, "dataset %s is not managed", row.Name)''',
 '''		return deletion{}, errorf(FailedPrecondition, "dataset %s is not managed: %s", row.Name, p.notManaged(row))'''),
('''			return deletion{}, errorf(FailedPrecondition, "volume %s has dependent clone %s", id, c)''',
 '''			return deletion{}, errorf(FailedPrecondition, "volume %s has dependent clone %s%s", id, c, uncommittedClone(t, s, c))'''),
])

sub('internal/endpoint/table.go', [
('''	// ownership, from user properties
	Managed        bool''', '''	// ownership, from user properties
	Managed bool
	// ManagedSource is the source of a managed_resource marker that does
	// not make the row managed ("received", "inherited from <ds>", or
	// "local" for a value other than true); "" when there is none or it
	// does. Messages only (Planner.notManaged).
	ManagedSource  string'''),
('''		r.NFSServer = own(cNFSServer)''', '''		if p := cell(cManaged); !r.Managed && userValue(p) != "" {
			r.ManagedSource = p.Source
		}
		r.NFSServer = own(cNFSServer)'''),
])
