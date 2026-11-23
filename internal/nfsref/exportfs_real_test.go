package nfsref

import (
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestExportfsMatchesReal diffs ExportfsRA against a real exportfs binary
// built from nfs-utils with _PATH_EXPORTS, _PATH_EXPORTS_D and the state
// directory pointed at NFSREF_ROOT/{exports,exports.d,state}:
//
//	NFSREF_EXPORTFS=/path/to/exportfs NFSREF_ROOT=/path/to/root go test -run TestExportfsMatchesReal ./internal/nfsref
//
// Without nfsd, nfs-utils reads no /proc/fs/nfsd/export_features and uses
// the old secinfo flags (ro, root_squash, all_squash), so the model does
// too here. Paths do not exist, so realpath keeps them.
func TestExportfsMatchesReal(t *testing.T) {
	bin, root := os.Getenv("NFSREF_EXPORTFS"), os.Getenv("NFSREF_ROOT")
	if bin == "" || root == "" {
		t.Skip("set NFSREF_EXPORTFS and NFSREF_ROOT")
	}
	if _, err := os.Stat("/proc/fs/nfsd/export_features"); err != nil {
		old := SecinfoFlags
		SecinfoFlags = ExpReadonly | ExpRootSquash | ExpAllSquash
		defer func() { SecinfoFlags = old }()
	}
	n := 3000
	if s := os.Getenv("NFSREF_N"); s != "" {
		n = atoiOr(s, n)
	}
	r := rand.New(rand.NewSource(int64(atoiOr(os.Getenv("NFSREF_SEED"), 1))))
	mismatches, crashed := 0, 0
	defer func() { t.Logf("%d cases, %d exportfs crashes skipped", n, crashed) }()
	for i := 0; i < n && mismatches < 5; i++ {
		etc, zfsx, early := randomExports(r), randomZFSExports(r), randomExports(r)
		if r.Intn(3) == 0 {
			etc = ""
		}
		write := func(p, s string) {
			if err := os.WriteFile(filepath.Join(root, p), []byte(s), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		write("exports", etc)
		write("exports.d/zfs.exports", zfsx)
		write("exports.d/00-early.exports", early)
		_ = os.Remove(filepath.Join(root, "state", "etab"))
		cmd := exec.Command(bin, "-ra")
		cmd.Dir = root
		stderr, _ := cmd.CombinedOutput()
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			// nfs-utils 3.1.1 aborts ("free(): double free") on some
			// inputs with a parse error; etab is then not rewritten.
			crashed++
			continue
		}
		got, _ := os.ReadFile(filepath.Join(root, "state", "etab"))
		files := ExportsFiles{Exports: []byte(etc), Dir: map[string][]byte{"zfs.exports": []byte(zfsx), "00-early.exports": []byte(early)}}
		want, _ := ExportfsRA(files, nil, nil)
		if string(got) != want {
			mismatches++
			t.Errorf("case %d\n/etc/exports:\n%s\n00-early.exports:\n%s\nzfs.exports:\n%s\nreal etab:\n%s\nmodel etab:\n%s\nexportfs said:\n%s",
				i, etc, early, zfsx, got, want, stderr)
		}
	}
}

func atoiOr(s string, d int) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return d
		}
		n = n*10 + int(c-'0')
	}
	if s == "" {
		return d
	}
	return n
}

var (
	rPaths = []string{"/tank/v/a", "/tank/v/a", "/tank/v", "/tank/with\\040space", "/tank/v/b", "/"}
	rHosts = []string{"10.0.0.0/24", "10.0.0.5/32", "10.0.0.5", "10.0.0.0/16", "10.0.0.0/024", "10.0.0.0/+24",
		"10.0.0.0/255.255.0.0", "10.0.0.5/24", "10.0.0.0/33", "10.0.0.0/", "*", "", "node-a.invalid", "*.lan",
		"@ng", "gss/krb5", "10.1", "0x0a.0.0.5", "010.0.0.0/8", "::1", "fe80::/64", "[::1]", "10.0.0.0/24 ",
		"#c", "-ro", "10.0.0.0/2x", "10.0.0.0/-1", "10.0.0.5/32", "\"10.0.0.0/24\"", "10.0.0.0/24\\\n  10.0.0.6"}
	rOpts = []string{"rw", "ro", "sec=sys,rw,no_subtree_check,mountpoint", "sec=sys,ro,no_subtree_check,mountpoint",
		"rw,no_root_squash", "ro,insecure", "rw,crossmnt", "sec=sys,ro,sec=krb5,rw", "sec=krb5:sys,rw",
		"ro,sec=sys,rw", "rw,anonuid=0,all_squash", "ro,nohide", "rw,sync", "ro,fsid=0", "rw,bogus",
		"ro,mountpoint=/x", "sec=(null),rw", "rw,anonuid=x", "", "rw,squash_uids=1-5", "sec=none,rw",
		"rw,fsid=0x10", "ro,refer=/a@b", "rw,xprtsec=tls:mtls", "rw,async,no_wdelay,nohide,insecure_locks,no_acl"}
)

func randomExports(r *rand.Rand) string {
	var b strings.Builder
	for range r.Intn(5) {
		b.WriteString(rPaths[r.Intn(len(rPaths))])
		for range 1 + r.Intn(3) {
			b.WriteString(" ")
			if r.Intn(6) == 0 {
				b.WriteString("-" + rOpts[r.Intn(len(rOpts))] + " ")
			}
			b.WriteString(rHosts[r.Intn(len(rHosts))])
			if r.Intn(8) != 0 {
				b.WriteString("(" + rOpts[r.Intn(len(rOpts))] + ")")
			}
		}
		b.WriteString("\n")
		if r.Intn(5) == 0 {
			b.WriteString("# comment\n\n")
		}
	}
	return b.String()
}

// randomZFSExports is zfs.exports after random sharenfs values.
func randomZFSExports(r *rand.Rand) string {
	toks := []string{"rw", "ro", "=", "=", ":", ":", ",", ",", "@", "*", "10.0.0.0/24", "10.0.0.5/32", "10.0.0.0/16",
		"10.1.0.0/024", "10.0.0.5", "0.0.0.0/0", "/", "[", "]", "::1", " ", "#", "sec", "sec=sys", "sec=krb5",
		"crossmnt", "no_root_squash", "insecure", "anonuid=0", "all_squash", "on", "node-a.invalid", "\t", "-", "(", ")", "\"", "nohide", "sync=", "fsid=x"}
	f := ""
	for range 1 + r.Intn(3) {
		var v strings.Builder
		for range 1 + r.Intn(8) {
			v.WriteString(toks[r.Intn(len(toks))])
		}
		if g, err := ShareFile(f, rPaths[r.Intn(len(rPaths))], v.String()); err == nil {
			f = g
		}
	}
	return f
}
