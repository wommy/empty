package zfsdiff

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
)

// Gen generates random sequences over a small namespace, so commands
// collide often: filesystems a, b, a/c, clones x, y, a/x, volumes v, a/w,
// the receive target r, snapshots s1, s2, hold tags t1, t2.
type Gen struct {
	// Full also generates what the simulator does not implement
	// (rename, destroy -R); off for model-vs-simulator runs.
	Full bool
	r    *rand.Rand
	m    *Model // the state so far, so picks favour names that exist
}

// NewGen returns a generator seeded with seed.
func NewGen(seed uint64) *Gen { return &Gen{r: rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))} }

var (
	genFS    = []string{"a", "b", "a/c", "x", "y", "a/x", "r"}
	genVol   = []string{"v", "a/w", "x", "y"}
	genHeads = []string{"a", "b", "a/c", "v", "a/w", "x", "y", "r"}
	genSnaps = []string{"s1", "s2"}
	genSizes = []string{"67108864", "134217728", "1000000", "0", "16384"}
	genSet   = [][]string{
		{"zd:a", "1", "2"}, {"zd:b", "x", "y"},
		{"sharenfs", "on", "off", "rw=@10.0.0.0/8"},
		{"canmount", "on", "off", "noauto"},
		{"mountpoint", "none", "legacy"},
		{"compression", "lz4", "off", "gzip-6", "zstd", "bogus"},
		{"recordsize", "65536", "1048576", "3000"},
		{"atime", "off", "on"}, {"sync", "always", "standard"},
		{"xattr", "on", "dir"}, {"snapdir", "visible"}, {"copies", "2"},
		{"logbias", "throughput"}, {"acltype", "posix"}, {"dedup", "off"},
		{"quota", "none", "0", "1073741824", "1048576"},
		{"refquota", "none", "0", "1073741824", "10485760"},
		{"reservation", "none", "10485760"},
		{"refreservation", "auto", "none", "10485760", "21474836480"},
		{"volsize", "134217728", "201326592", "1000000", "0"},
		{"volblocksize", "8192", "65536"},
	}
	genInherit = []string{"zd:a", "zd:b", "sharenfs", "compression", "mountpoint", "atime",
		"canmount", "refquota", "refreservation", "volsize", "volblocksize", "copies", "bogus"}
)

func (g *Gen) pick(s []string) string { return s[g.r.IntN(len(s))] }
func (g *Gen) coin(p float64) bool    { return g.r.Float64() < p }

func (g *Gen) prop() string {
	p := genSet[g.r.IntN(len(genSet))]
	return p[0] + "=" + p[1+g.r.IntN(len(p)-1)]
}

// existing returns an existing dataset of the kinds in mask below the
// generation root, relative to it, or "".
func (g *Gen) existing(mask kind) string {
	if g.m == nil {
		return ""
	}
	var names []string
	for k, d := range g.m.ds {
		if d.kind&mask != 0 && strings.HasPrefix(k, genRoot+"/") {
			names = append(names, k[len(genRoot)+1:])
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return names[g.r.IntN(len(names))]
}

func (g *Gen) snap() string {
	if g.coin(0.7) {
		if e := g.existing(kSnap); e != "" {
			return "$R/" + e
		}
	}
	return "$R/" + g.pick(genHeads) + "@" + g.pick(genSnaps)
}

func (g *Gen) head() string {
	if g.coin(0.6) {
		if e := g.existing(kFS | kVol); e != "" {
			return "$R/" + e
		}
	}
	return "$R/" + g.pick(genHeads)
}

// genRoot is where Seq runs its scratch model.
const genRoot = "gen/r"

// Step returns one random step.
func (g *Gen) Step() Step {
	a := []string{}
	add := func(s ...string) { a = append(a, s...) }
	switch w := g.r.IntN(100); {
	case w < 12: // create filesystem
		add("create")
		if g.coin(0.25) {
			add("-p")
		}
		if g.coin(0.2) {
			add("-u") // left unmounted, for mount steps
		}
		if g.coin(0.3) {
			add("-o", g.prop())
		}
		add("$R/" + g.pick(genFS))
	case w < 22: // create volume
		add("create")
		if g.coin(0.4) {
			add("-s")
		}
		if g.coin(0.15) {
			add("-p")
		}
		add("-V", g.pick(genSizes))
		if g.coin(0.2) {
			add("-o", "volblocksize="+g.pick([]string{"8192", "65536", "3000"}))
		}
		if g.coin(0.25) {
			add("-o", "refreservation="+g.pick([]string{"auto", "none", "10485760"}))
		}
		add("$R/" + g.pick(genVol))
	case w < 34: // snapshot
		add("snapshot")
		if g.coin(0.2) {
			add("-r")
		}
		if g.coin(0.15) {
			add("-o", g.pick([]string{"zd:a=snap", "compression=off"}))
		}
		add(g.snap())
	case w < 44: // clone
		add("clone")
		if g.coin(0.25) {
			add("-o", g.pick([]string{"refreservation=auto", "zd:b=c", "volblocksize=8192", "sharenfs=on", "canmount=noauto"}))
		}
		add(g.snap(), "$R/"+g.pick([]string{"x", "y", "a/x"}))
	case w < 56: // destroy
		add("destroy")
		flags := []string{"", "", "-r", "-d", "-rd"}
		if g.Full {
			flags = append(flags, "-R")
		}
		if f := g.pick(flags); f != "" {
			add(f)
		}
		if g.coin(0.5) {
			add(g.snap())
		} else {
			add(g.head())
		}
	case w < 72: // set
		add("set")
		if g.coin(0.2) {
			add("-u")
		}
		add(g.prop())
		if g.coin(0.15) {
			add(g.prop())
		}
		if g.coin(0.1) {
			add(g.snap())
		} else {
			add(g.head())
		}
	case w < 79: // inherit
		add("inherit")
		if g.coin(0.2) {
			add("-r")
		}
		if g.coin(0.25) {
			add("-S")
		}
		add(g.pick(genInherit), g.head())
	case w < 86: // hold / release
		add(g.pick([]string{"hold", "hold", "release"}))
		if g.coin(0.15) {
			add("-r")
		}
		add(g.pick([]string{"t1", "t2"}), g.snap())
	case w < 90 && !g.Full: // mount (rename's share when Full)
		add("mount", g.head())
	case w < 90: // rename
		add("rename")
		if g.coin(0.5) {
			add(g.snap(), "$R/"+g.pick(genHeads)+"@"+g.pick([]string{"s2", "s3"}))
		} else {
			add(g.head(), "$R/"+g.pick([]string{"b", "z", "a/z", "a/c/z"}))
		}
	case w < 95:
		add("promote", "$R/"+g.pick([]string{"x", "y", "a/x", "a"}))
	default:
		return Step{[]string{"@sendrecv", g.snap(), "$R/r"}}
	}
	return Step{a}
}

// Seq returns a random sequence of n steps, starting with a few creates.
// It runs the steps on a scratch model as it goes, so later steps favour
// datasets that exist.
func (g *Gen) Seq(name string, n int) Seq {
	s := Seq{Name: name}
	s.Steps = append(s.Steps,
		Step{[]string{"create", "$R/a"}},
		Step{[]string{"create", "-V", "67108864", "$R/v"}},
		Step{[]string{"snapshot", "$R/a@s1"}},
	)
	g.m = NewModel("gen", RootProps)
	g.apply(Step{[]string{"create", "-p", "$R"}})
	for _, st := range s.Steps {
		g.apply(st)
	}
	for len(s.Steps) < n {
		st := g.Step()
		s.Steps = append(s.Steps, st)
		g.apply(st)
	}
	g.m = nil
	s.Notes = []string{fmt.Sprintf("generated: %s", name)}
	return s
}

// apply runs st on the scratch model (no projection: generation only
// needs the state).
func (g *Gen) apply(st Step) {
	args := make([]string, len(st.Args))
	for i, a := range st.Args {
		args[i] = strings.ReplaceAll(a, "$R", genRoot)
	}
	if args[0] == "@sendrecv" {
		_ = g.m.SendRecv(args[1], args[2])
		return
	}
	_, _ = g.m.Run(context.Background(), "zfs", args...)
}
