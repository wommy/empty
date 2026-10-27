package main

import (
	"context"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/zfs"
)

// F5: `zfs set sharenfs=on tank` exports /tank rw,crossmnt to everyone,
// and with it every volume the endpoint would create under tank/k8s,
// whatever the volume's own sharenfs. Startup refuses (78: restarting
// cannot fix it) and names the line; an ancestor shared without crossmnt
// does not stop it.
func TestStartupRefusesACrossmntAncestor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		share   string
		refused bool
	}{
		{"on", true},               // libshare: rw,crossmnt to "*"
		{"rw=@10.0.0.0/24", false}, // our form: no crossmnt
		{"crossmnt,ro=@10.9.0.0/16", true},
	} {
		t.Run(tc.share, func(t *testing.T) {
			t.Parallel()
			d, sim := simDeps(t)
			if err := zfs.New(sim).Set(context.Background(), "tank", map[string]string{"sharenfs": tc.share}); err != nil {
				t.Fatal(err)
			}
			err := run(context.Background(), []string{"-parent", "tank/k8s", "-listen", notASocket(t), "-parent-wait", "0"}, d)
			refused := exitCode(err) == exitConfig && strings.Contains(err.Error(), "crossmnt NFS export") && strings.Contains(err.Error(), "/tank ")
			if refused != tc.refused {
				t.Errorf("exit %d %v; refused %v, want %v", exitCode(err), err, refused, tc.refused)
			}
		})
	}
}
