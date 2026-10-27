package weightstest

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// One run of two parts, nproc 4. Part 0 is the loaded one: its 4.00
// measured seconds are 3000 forks; part 1's 2.00 seconds are 1000 forks.
//
//	A  sequential                       own 2.0           -> 2.00 s
//	B  parallel                         cost 8.0 / 4      -> 2.00 s
//	C  sequential                       own 1.0           -> 1.00 s
//	D  sequential, parallel subtests    cost 4.0 / 4      -> 1.00 s (not its wall, 1.5)
//
// Normalized: part 0 is 750 forks per s, part 1 500, so A = B = 1500 and
// C = D = 500 forks; rescaled to the raw total of 6.00 s: A = B = 2.25,
// C = D = 0.75. Raw (no fork counts): 2, 2, 1, 1.
const (
	part0 = "# shard test status parallel own_s cost_s wall_s subtests (ci/test-times.awk)\n" +
		"# nproc=4 mode=pr run=7.1 part=0/2\n" +
		"endpoint.0\tA\tPASS\t0\t2.000\t2.000\t2.010\t0\n" +
		"endpoint.1\tB\tPASS\t1\t0.500\t8.000\t2.600\t8\n"
	part1 = "# shard test status parallel own_s cost_s wall_s subtests (ci/test-times.awk)\n" +
		"# nproc=4 mode=pr run=7.1 part=1/2\n" +
		"endpoint.3\tC\tPASS\t0\t1.000\t1.000\t1.010\t0\n" +
		"endpoint.4\tD\tPASS\t0\t0.000\t4.000\t1.500\t6\n"
	oldWeights = "# note: E is set by hand.\n#\n# <test> <seconds>\nA 9.00\nE 0.30\n"
)

var (
	normalized = map[string]float64{"A": 2.25, "B": 2.25, "C": 0.75, "D": 0.75, "E": 0.30}
	raw        = map[string]float64{"A": 2, "B": 2, "C": 1, "D": 1, "E": 0.30}
)

// load is the guest's load report as ci/real-zfs-guest.sh writes it
func load(forks string, cpu string) string {
	s := "guest load over the test phase: 5.0s on 4 vCPUs\n"
	if forks != "" {
		s += "forks " + forks + " (600/s, 5.0 CPU-ms each), context switches 1000 (200/s)\n"
	}
	s += "cgroup CPU-s over the phase (cpu.stat usage, of it system):\n"
	if cpu != "" {
		s += "  root                 " + cpu + "  (sys 1.00)\n"
	}
	return s + "  tests                 1.00  (sys 0.50)\n"
}

// jobLog wraps a part's load report and TSV as a GitHub job log
func jobLog(loadTxt, tsv string) string {
	var b strings.Builder
	b.WriteString("\ufeff")
	line := func(s string) { b.WriteString("2026-10-27T17:38:54.8816381Z " + s + "\n") }
	line("Current runner version: '2.338.0'")
	for _, l := range strings.Split(strings.TrimSuffix(loadTxt, "\n"), "\n") {
		line(l)
	}
	line("##[group]test-times.tsv")
	for _, l := range strings.Split(strings.TrimSuffix(tsv, "\n"), "\n") {
		line(l)
	}
	line("##[endgroup]")
	return b.String()
}

func write(t *testing.T, path, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run runs a copy of the script in a scratch tree (it writes
// ci/real-zfs-weights.txt next to itself) and returns its weights
func run(t *testing.T, files func(dir string) []string) (map[string]float64, string, string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	dir := t.TempDir()
	for _, f := range []string{"real-zfs-weights.sh", "test-times.awk"} {
		b, err := os.ReadFile(filepath.Join("..", f))
		if err != nil {
			t.Fatal(err)
		}
		write(t, filepath.Join(dir, "ci", f), string(b))
	}
	write(t, filepath.Join(dir, "ci", "real-zfs-weights.txt"), oldWeights)
	cmd := exec.Command("bash", append([]string{filepath.Join(dir, "ci", "real-zfs-weights.sh"), "-s", "fixture"}, files(dir)...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("real-zfs-weights.sh: %v\n%s", err, out)
	}
	b, err := os.ReadFile(filepath.Join(dir, "ci", "real-zfs-weights.txt"))
	if err != nil {
		t.Fatal(err)
	}
	w := map[string]float64{}
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) != 2 || strings.HasPrefix(l, "#") {
			continue
		}
		x, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			t.Fatalf("bad row %q", l)
		}
		w[f[0]] = x
	}
	return w, string(b), string(out)
}

func check(t *testing.T, got, want map[string]float64) {
	t.Helper()
	for k, v := range want {
		if g, ok := got[k]; !ok || math.Abs(g-v) > 0.005 {
			t.Errorf("%s = %.2f (present %v), want %.2f", k, g, ok, v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d weights %v, want %d", len(got), got, len(want))
	}
}

func TestTSVsNormalizedByForks(t *testing.T) {
	w, file, _ := run(t, func(dir string) []string {
		write(t, filepath.Join(dir, "a", "p0", "test-times.tsv"), part0)
		write(t, filepath.Join(dir, "a", "p0", "load.txt"), load("3000", "30.00"))
		write(t, filepath.Join(dir, "a", "p1", "test-times.tsv"), part1)
		write(t, filepath.Join(dir, "a", "p1", "load.txt"), load("1000", "20.00"))
		return []string{filepath.Join(dir, "a", "p0", "test-times.tsv"), filepath.Join(dir, "a", "p1", "test-times.tsv")}
	})
	check(t, w, normalized)
	for _, s := range []string{"# note: E is set by hand.\n", "# Source: fixture.\n"} {
		if !strings.Contains(file, s) {
			t.Errorf("weights file lacks %q:\n%s", s, file)
		}
	}
}

func TestJobLogsNormalizedByForks(t *testing.T) {
	w, _, _ := run(t, func(dir string) []string {
		write(t, filepath.Join(dir, "l", "ep0.log"), jobLog(load("3000", "30.00"), part0))
		write(t, filepath.Join(dir, "l", "ep1.log"), jobLog(load("1000", "20.00"), part1))
		return []string{filepath.Join(dir, "l", "ep0.log"), filepath.Join(dir, "l", "ep1.log")}
	})
	check(t, w, normalized)
}

// without a fork count, the root cgroup's CPU-seconds stand in: 30 and 10
// CPU-s are the same 3:1 as 3000 and 1000 forks
func TestCPUSecondsWithoutForks(t *testing.T) {
	w, _, _ := run(t, func(dir string) []string {
		write(t, filepath.Join(dir, "l", "ep0.log"), jobLog(load("", "30.00"), part0))
		write(t, filepath.Join(dir, "l", "ep1.log"), jobLog(load("", "10.00"), part1))
		return []string{filepath.Join(dir, "l", "ep0.log"), filepath.Join(dir, "l", "ep1.log")}
	})
	check(t, w, normalized)
}

func TestRawWithoutLoad(t *testing.T) {
	w, _, stderr := run(t, func(dir string) []string {
		write(t, filepath.Join(dir, "p0.tsv"), part0)
		write(t, filepath.Join(dir, "p1.tsv"), part1)
		return []string{filepath.Join(dir, "p0.tsv"), filepath.Join(dir, "p1.tsv")}
	})
	check(t, w, raw)
	if !strings.Contains(stderr, "not normalized per part") {
		t.Errorf("no warning that the parts stay raw seconds:\n%s", stderr)
	}
}
