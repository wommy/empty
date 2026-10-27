p = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/realfacts_test.go'
s = open(p).read()


def rep(a, b):
    global s
    assert s.count(a) == 1, a
    s = s.replace(a, b)


rep('''	{row: "CR-03", what: "`destroy -r` stopped by a held snapshot has already destroyed others (not atomic)",
		simDiverges: "the Sim refuses `destroy -r` atomically",
		check: func(f *factEnv) error {''', '''	// wave8 believed the opposite (not atomic) without a pool to check it;
	// zfs_main batches a filesystem's snapshots into one lzc_destroy_snaps,
	// which destroys none when any is held (libzfs_core.c). The endpoint
	// never runs -r; the fact pins that a held snapshot leaves the tree whole.
	{row: "CR-03", what: "`destroy -r` stopped by a held snapshot destroys none of that filesystem's snapshots",
		check: func(f *factEnv) error {''')
rep('''			if f.exists(f.p + "/a@s1") {
				return errors.New("unheld snapshot survived: destroy -r was atomic")
			}''', '''			if !f.exists(f.p+"/a@s1") || !f.exists(f.p+"/a") {
				return errors.New("destroy -r of a held tree destroyed part of it")
			}''')
rep('''	_ = strings.TrimSpace
}''', '''}''')
rep('''	"strconv"
	"strings"
	"testing"''', '''	"strconv"
	"testing"''')
open(p, 'w').write(s)
