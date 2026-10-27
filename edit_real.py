p='internal/zfs/zfsdiff/real.go'
s=open(p).read()
def rep(a,b):
    global s
    assert s.count(a)==1,a
    s=s.replace(a,b)
rep('''	JSON             []string
	Err              error
}''','''	JSON             []string
	Err              error
	// NotRun: the replay's start budget ran out before this sequence.
	NotRun bool
}''')
rep('''	case r.Err != nil:
		return "harness-error"''','''	case r.NotRun:
		return "not-run"
	case r.Err != nil:
		return "harness-error"''')
rep('''// ReplayAll runs seqs on target under base/s<i> (workers at a time) and
// on a fresh simulator and model for each, with pool as their pool.
func ReplayAll(ctx context.Context, target Target, pool, base string, seqs []Seq, workers int) []*Replay {''','''// ReplayAll runs seqs on target under base/s<i> (workers at a time) and
// on a fresh simulator and model for each, with pool as their pool. A
// sequence not started by startBy (zero: no limit) is NotRun. Each
// sequence's cleanup runs on a context of its own, not on ctx: a replay
// cut short by ctx must still leave nothing behind on the pool.
func ReplayAll(ctx context.Context, target Target, pool, base string, seqs []Seq, workers int, startBy time.Time) []*Replay {''')
rep('''				root := base + "/s" + strconv.Itoa(i)
				rp := &Replay{Seq: seqs[i]}
''','''				root := base + "/s" + strconv.Itoa(i)
				rp := &Replay{Seq: seqs[i]}
				if ctx.Err() != nil || !startBy.IsZero() && time.Now().After(startBy) {
					rp.NotRun = true
					out[i] = rp
					continue
				}
''')
rep('''				if err := target.Cleanup(ctx, root); err != nil {
					rp.Err = fmt.Errorf("cleanup %s: %w", root, err)
				}''','''				cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CleanupTimeout)
				if err := target.Cleanup(cctx, root); err != nil {
					rp.Err = fmt.Errorf("cleanup %s: %w", root, err)
				}
				cancel()''')
rep('''// RealTarget is''','''// CleanupTimeout bounds one cleanup, which runs even after the replay's
// own context has expired.
const CleanupTimeout = 60 * time.Second

// RealTarget is''')
rep('''	"sync"
''','''	"sync"
	"time"
''')
rep('''		if r.Verdict() == "agree" && len(r.JSON) == 0 {''','''		if v := r.Verdict(); (v == "agree" || v == "not-run") && len(r.JSON) == 0 {''')
open(p,'w').write(s)
