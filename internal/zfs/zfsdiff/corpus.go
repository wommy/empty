package zfsdiff

import (
	"embed"
	"io/fs"
	"path"
	"slices"
	"sort"
	"strings"
)

// corpus is testdata/zfsdiff/*.txt, embedded so the real-pool replay runs
// from a test binary copied alone into the CI VM.
//
//go:embed testdata/zfsdiff/*.txt
var corpus embed.FS

// Corpus returns the recorded sequences, sorted by name.
func Corpus() ([]Seq, error) {
	files, err := fs.Glob(corpus, "testdata/zfsdiff/*.txt")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	out := make([]Seq, 0, len(files))
	for _, f := range files {
		b, err := corpus.ReadFile(f)
		if err != nil {
			return nil, err
		}
		s, err := Parse(strings.TrimSuffix(path.Base(f), ".txt"), string(b))
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// PRSample is the corpus subset the PR tier replays on the real pool
// (ADR 0005: the VM job stays under a minute; the whole corpus replays
// nightly). It is about a fifth of the corpus's zfs invocations (253 of
// 1,272), chosen for what the endpoint does and what is unsettled: its
// Expand, shrink and clone patterns, the three ambiguities, mount
// changelists and its mount-then-commit resume, holds and deferred
// destroys, the space model. No step turns a share on: on PR the replay
// shares a guest with the NFS tests but not their shard, and a share
// would run exportfs -ra on their export table (the nightly full replay
// is sharded with them).
var PRSample = []string{
	"expand-pattern", "shrink-pattern", "create-vol-resv-auto", "refquota-zero",
	"recv-quota-none", "defer-endpoint-clone-vol", "clone-vol-default-resv",
	"volsize-tracks-thick", "canmount-noauto-mounted", "mountpoint-remount",
	"share-off-unmounted", "recv-canmount-off", "hold-destroy", "destroy-r-held-child",
	"destroy-r-deferred-origin-inside", "create-vol-rounding", "resv-too-big",
	"volsize-enospc-revert", "promote-conflict", "inherit-native-from-pool",
	"mount-endpoint-resume",
}

// Sample returns the sequences of seqs named in names, in seqs' order.
func Sample(seqs []Seq, names []string) []Seq {
	var out []Seq
	for _, s := range seqs {
		if slices.Contains(names, s.Name) {
			out = append(out, s)
		}
	}
	return out
}
