package zfs

import (
	"context"
	"strings"
	"testing"
)

// scriptRunner answers by the joined argv.
type scriptRunner map[string]string

func (s scriptRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, ok := s[strings.Join(args, " ")]
	if !ok {
		// what OpenZFS 2.2 prints for an unknown flag
		return nil, &ExitError{Args: args, Code: 2, Stderr: "invalid option 'j'\nusage:\n"}
	}
	return []byte(out), nil
}

func TestVersion(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		runner scriptRunner
		want   Version
		ok     bool // Require("2.4") passes
	}{
		{"json 2.4", scriptRunner{"version -j": `{"output_version":{"command":"zfs version","vers_major":0,"vers_minor":1},"zfs_version":{"userland":"zfs-2.4.0-1","kernel":"zfs-kmod-2.4.0-1"}}`},
			Version{"zfs-2.4.0-1", "zfs-kmod-2.4.0-1"}, true},
		{"json mixed", scriptRunner{"version -j": `{"zfs_version":{"userland":"zfs-2.4.1-1","kernel":"zfs-kmod-2.3.4-1"}}`},
			Version{"zfs-2.4.1-1", "zfs-kmod-2.3.4-1"}, false},
		{"text 2.2 (no -j)", scriptRunner{"version": "zfs-2.2.2-0ubuntu9\nzfs-kmod-2.2.2-0ubuntu9\n"},
			Version{"zfs-2.2.2-0ubuntu9", "zfs-kmod-2.2.2-0ubuntu9"}, false},
		{"2.40 is not 2.4", scriptRunner{"version -j": `{"zfs_version":{"userland":"zfs-2.40.0","kernel":"zfs-kmod-2.40.0"}}`},
			Version{"zfs-2.40.0", "zfs-kmod-2.40.0"}, false},
	} {
		v, err := New(tc.runner).Version(ctx)
		if err != nil || v != tc.want {
			t.Errorf("%s: %+v %v", tc.name, v, err)
			continue
		}
		if err := v.Require("2.4"); (err == nil) != tc.ok {
			t.Errorf("%s: Require: %v", tc.name, err)
		}
	}
	// module not loaded: both forms fail, and the error says so
	if _, err := New(scriptRunner{}).Version(ctx); err == nil {
		t.Error("no version: no error")
	}
	if _, err := New(scriptRunner{"version -j": `{"zfs_version":{}}`}).Version(ctx); err == nil {
		t.Error("empty json: no error")
	}
}
