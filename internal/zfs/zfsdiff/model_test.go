package zfsdiff

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// TestModelRules pins the model's reading of OpenZFS 2.4.4: each case is
// a script, the last step's exit code and class, and projection lines that
// must (or, with a leading "!", must not) appear after it.
func TestModelRules(t *testing.T) {
	cases := []struct {
		name  string
		steps string
		code  int
		class Class
		lines []string
	}{
		{"thick zvol reservation is zvol_volsize_to_reservation (libzfs_dataset.c:5587)",
			"zfs create -V 67108864 $R/v", 0, "", []string{"$R/v\trefreservation\t70254592\tlocal", "$R/v\tvolsize\t67108864\tlocal"}},
		{"create -V rounds up to volblocksize (zfs_main.c:1236)",
			"zfs create -s -V 1000000 $R/v", 0, "", []string{"$R/v\tvolsize\t1015808\tlocal", "$R/v\trefreservation\t0\tdefault"}},
		{"create -p is type-specific (zfs_main.c:1284 zfs_dataset_exists(name, type))",
			"zfs create -V 67108864 $R/v; zfs create -p $R/v", 1, "exists", nil},
		{"create -p on an existing filesystem is a no-op",
			"zfs create $R/a; zfs create -p -o zd:a=1 $R/a", 0, "", []string{"$R/a\tzd:a\t-\t-"}},
		{"2.4.4 create sends refreservation=auto as UINT64_MAX: ENOSPC (no zfs_create_fix_auto_resv)",
			"zfs create -V 67108864 -o refreservation=auto $R/v", 1, "nospace", []string{"!$R/v\ttype\tvolume\t-"}},
		{"clone -o refreservation=auto uses the snapshot's volsize (libzfs_dataset.c:4052)",
			"zfs create -V 67108864 $R/v; zfs snapshot $R/v@s1; zfs set volsize=134217728 $R/v; zfs clone -o refreservation=auto $R/v@s1 $R/x",
			0, "", []string{"$R/x\trefreservation\t70254592\tlocal", "$R/v@s1\tvolsize\t67108864\tlocal"}},
		{"synthetic refreservation tracks volsize (libzfs_dataset.c:1603)",
			"zfs create -V 67108864 $R/v; zfs set volsize=134217728 $R/v", 0, "", []string{"$R/v\trefreservation\t138412032\tlocal"}},
		{"ENOSPC on the synthetic refreservation puts volsize back (libzfs_dataset.c:1871)",
			"zfs create -V 67108864 $R/v; zfs set volsize=21474836480 $R/v", 1, "nospace-resv", []string{"$R/v\tvolsize\t67108864\tlocal"}},
		{"quota/refquota=0 must be spelled none (libzfs_util.c:1826)",
			"zfs create $R/a; zfs set refquota=0 $R/a", 1, "badprop", nil},
		{"refquota=none is a local 0 (dsl_prop_get_all, then the stats add the value only if absent)",
			"zfs create $R/a; zfs set refquota=none $R/a", 0, "", []string{"$R/a\trefquota\t0\tlocal"}},
		{"refquota is set before refreservation and fails against the old one (dsl_dataset.c:4487)",
			"zfs create -o refquota=20971520 -o refreservation=20971520 $R/a; zfs set refquota=10485760 refreservation=10485760 $R/a",
			1, "nospace-quota", []string{"$R/a\trefquota\t20971520\tlocal", "$R/a\trefreservation\t10485760\tlocal"}},
		{"plain inherit on a received dataset hides the received value (dsl_prop.c:795-810)",
			"zfs create -o zd:a=1 $R/a; zfs snapshot $R/a@s1; @sendrecv $R/a@s1 $R/r; zfs set zd:a=2 $R/r; zfs inherit zd:a $R/r",
			0, "", []string{"$R/r\tzd:a\t-\t-"}},
		{"inherit -S reverts to the received value",
			"zfs create -o zd:a=1 $R/a; zfs snapshot $R/a@s1; @sendrecv $R/a@s1 $R/r; zfs set zd:a=2 $R/r; zfs inherit -S zd:a $R/r",
			0, "", []string{"$R/r\tzd:a\t1\treceived"}},
		{"sharenfs on an unmounted canmount=on filesystem mounts it (libzfs_changelist.c:258-268)",
			"zfs create -u $R/a; zfs set sharenfs=on $R/a", 0, "", []string{"$R/a\tmounted\tyes\t-"}},
		{"set -u gathers with CL_GATHER_DONT_UNMOUNT: no mount (libzfs_changelist.c:193)",
			"zfs create -u $R/a; zfs set -u sharenfs=on $R/a", 0, "", []string{"$R/a\tmounted\tno\t-"}},
		{"canmount=noauto does not unmount (libzfs_dataset.c:1814-1819)",
			"zfs create $R/a; zfs set canmount=noauto $R/a", 0, "", []string{"$R/a\tmounted\tyes\t-"}},
		{"destroy -d of a cloned snapshot defers; the clone's destroy reaps it (dsl_destroy.c rmorigin)",
			"zfs create $R/a; zfs snapshot $R/a@s1; zfs clone $R/a@s1 $R/x; zfs destroy -d $R/a@s1; zfs destroy $R/x",
			0, "", []string{"!$R/a@s1\ttype\tsnapshot\t-"}},
		{"destroy -r refuses an outside clone (zfs_main.c:1439)",
			"zfs create $R/a; zfs snapshot $R/a@s1; zfs clone $R/a@s1 $R/x; zfs destroy -r $R/a", 1, "clones", nil},
		{"snapshot of a missing dataset is a usage error (zfs_main.c:4697)",
			"zfs snapshot $R/a@s1", 2, "usage", nil},
		{"a zvol snapshot's filesystem-only properties are '-' (headcheck, libzfs_dataset.c:2131)",
			"zfs create -V 67108864 $R/v; zfs snapshot $R/v@s1", 0, "", []string{"$R/v@s1\txattr\t-\t-"}},
		{"native inheritance from the pool root",
			"zfs create $R/a", 0, "", []string{"$R/a\tcompression\tlz4\tinherited from zfsep-test"}},
		{"promote moves older snapshots and swaps origins (dsl_dataset.c promote)",
			"zfs create $R/a; zfs snapshot $R/a@s1; zfs snapshot $R/a@s2; zfs clone $R/a@s2 $R/x; zfs promote $R/x",
			0, "", []string{"$R/x@s1\ttype\tsnapshot\t-", "$R/a\torigin\t$R/x@s2\t-", "$R/x\torigin\t-\t-"}},
	}
	for _, c := range cases {
		seq, err := Parse(c.name, strings.ReplaceAll(c.steps, "; ", "\n"))
		if err != nil {
			t.Fatal(err)
		}
		tr := Exec(context.Background(), NewModelRunner(testPool), testRoot, seq)
		last := tr[len(tr)-1]
		if last.Code != c.code || last.Class != c.class {
			t.Errorf("%s: got exit %d %s %q", c.name, last.Code, last.Class, last.Stderr)
		}
		for _, l := range c.lines {
			if want := !strings.HasPrefix(l, "!"); slices.Contains(last.Proj, strings.TrimPrefix(l, "!")) != want {
				t.Errorf("%s: line %q present=%v\n%s", c.name, l, !want, Render(tr))
			}
		}
	}
}
