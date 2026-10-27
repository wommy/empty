S = '/tmp/claude-0/-home-user-empty/d2383434-1d2e-5ac5-931d-d25ccb26a882/scratchpad'
W = '/home/user/empty/.claude/worktrees/agent-a5e3d463acd0910f8/internal/endpoint/'
src = open(S + '/wave13/probe-src/fsm_explore_test.fixed.go').read()
lines = src.split('\n')


def find(prefix, start=0):
    for i in range(start, len(lines)):
        if lines[i].startswith(prefix):
            return i
    raise KeyError(prefix)


c0 = find('// state of one row, by the spec')
c1 = find('type snapshotOf struct')
classify_block = '\n'.join(lines[c0:c1]).rstrip() + '\n'
rest = lines[:c0] + lines[c1:]
rest = '\n'.join(rest)

# remove consts moved to the classify file
for a in ['const fsmParent = "tank/k8s/vols"\nconst dcPrefix = "democratic-csi"\n\n']:
    assert rest.count(a) == 1, a
    rest = rest.replace(a, '')

head = '''package endpoint

// The recovery FSM explorer's row classifier: every row a world can hold
// maps to a named state, or to IMPOSSIBLE(...), which the explorer reports
// as a finding. Pure (a Row and its Table in, a state name out), so its
// table test runs on every PR; the explorer itself is nightly
// (fsm_explore_test.go, build tag nightly).

import (
	"fmt"
	"strings"

	"github.com/wommy/empty/internal/zfs"
)

const fsmParent = "tank/k8s/vols"
const dcPrefix = "democratic-csi"

'''
open(W + 'fsm_classify_test.go', 'w').write(head + classify_block)

rest = rest.replace('package endpoint\n', '//go:build nightly\n\npackage endpoint\n', 1)
open(W + 'fsm_explore_test.go', 'w').write(rest)
