package endpoint_test

// Ported adversarial tests (scratchpad adv4/export.md, written against
// bdf89d0). The originals asserted the wrong behavior; these assert the
// fixed one, so each fails on 2246735 and passes after the fix. They build
// their own simulator, wrapped in an emulation of nfs-utils, and run on
// every leg.

import (
	"bufio"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/zfs"
	"github.com/wommy/empty/internal/zfs/zfstest"
)

// nfsUtils post-processes the simulator's etab the way nfs-utils exportfs
// writes it (support/export/client.c client_lookup, xtab.c):
//   - a client containing '/' is a subnet (MCL_SUBNETWORK): kept verbatim,
//     never resolved, never merged;
//   - any other client (MCL_FQDN: a name or bare address) that does not
//     resolve is skipped ("Failed to resolve"), and one that does is written
//     under the canonical name of the FIRST client (across /etc/exports,
//     then exports.d) with the same address.
//
// extra holds lines from /etc/exports for the same paths.
type nfsUtils struct {
	sim *zfstest.Sim
	// name -> ip; absent = unresolvable. IPs resolve to themselves.
	dns map[string]string
	// canonical name per ip, from an earlier export line
	firstName map[string]string
	extra     string
}

func (n nfsUtils) ReadEtab(ctx context.Context) ([]byte, error) {
	b, err := n.sim.ReadEtab(ctx)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	out.WriteString(n.extra)
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := sc.Text()
		path, rest, _ := strings.Cut(line, "\t")
		host, opts, _ := strings.Cut(rest, "(")
		if !strings.Contains(host, "/") { // MCL_FQDN (names and bare IPs)
			ip, ok := n.dns[host]
			if !ok && strings.Count(host, ".") == 3 && strings.Trim(host, "0123456789.") == "" {
				ip, ok = host, true
			}
			if !ok {
				continue // Failed to resolve: entry dropped, exportfs still "succeeds"
			}
			if c, ok := n.firstName[ip]; ok {
				host = c
			}
		}
		out.WriteString(path + "\t" + host + "(" + opts + "\n")
	}
	return []byte(out.String()), nil
}

func (n nfsUtils) NFSDThreads(ctx context.Context) (int, error) { return n.sim.NFSDThreads(ctx) }

const advPath = "/tank/k8s/vols/pvc"

func advSetup(t *testing.T, u nfsUtils) *endpoint.Endpoint {
	t.Helper()
	ctx := context.Background()
	sim := zfstest.New("tank")
	z := zfs.New(sim)
	if err := z.CreateFilesystem(ctx, "tank/k8s/vols", nil, true); err != nil {
		t.Fatal(err)
	}
	u.sim = sim
	ep, err := endpoint.New(z, endpoint.Config{Parent: "tank/k8s/vols", LockWait: time.Second, FileExport: true, Etab: u})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "pvc"}); err != nil {
		t.Fatal(err)
	}
	return ep
}

// F3 (was F1a): Lookup's prefix rule let "node-a" match the different
// client "node-a.evil.example", so Unpublish(node-a) was Unavailable
// forever. Names are refused now (F1), and Lookup has no prefix rule.
func TestAdv4_PrefixMatchUnpublishLivelock(t *testing.T) {
	ctx := context.Background()
	ep := advSetup(t, nfsUtils{dns: map[string]string{"node-a": "10.0.0.5", "node-a.evil.example": "10.6.6.6"}})
	for _, h := range []string{"node-a", "node-a.evil.example"} {
		if _, err := ep.PublishFileExport(ctx, "pvc", h, false); endpoint.CodeOf(err) != endpoint.InvalidArgument {
			t.Fatalf("publish %s: %v, want InvalidArgument", h, err)
		}
		if err := ep.UnpublishFileExport(ctx, "pvc", h); endpoint.CodeOf(err) != endpoint.InvalidArgument {
			t.Fatalf("unpublish %s: %v, want InvalidArgument", h, err)
		}
	}
	e := endpoint.ParseEtab([]byte(advPath + "\tnode-a.evil.example(rw)\n"))
	if c, ok := e.Lookup(advPath, "node-a"); ok {
		t.Fatalf("node-a matched %v", c)
	}
}

// F3 (was F1b): with node-a unresolvable and node-a.evil.example served ro
// (an /etc/exports line), Publish(node-a, ro) reported success from the
// other client's line.
func TestAdv4_PrefixMatchPublishFalseSuccess(t *testing.T) {
	ctx := context.Background()
	ep := advSetup(t, nfsUtils{dns: map[string]string{"node-a.evil.example": "10.6.6.6"},
		extra: advPath + "\tnode-a.evil.example(ro,sync)\n"})
	for _, ro := range []bool{true, false} {
		if _, err := ep.PublishFileExport(ctx, "pvc", "node-a", ro); err == nil {
			t.Fatalf("publish node-a ro=%v succeeded with nothing serving node-a", ro)
		}
	}
}

// F2: nfs-utils merges a bare-address client with an earlier hostname
// client of the same address and writes it under that name, so a publish
// to 10.0.0.5 never verified. Written as 10.0.0.5/32 it is a subnet:
// verbatim in etab, no merge.
func TestAdv4_AddressDedupPublishNeverVerifies(t *testing.T) {
	ctx := context.Background()
	ep := advSetup(t, nfsUtils{dns: map[string]string{"node-a": "10.0.0.5"},
		firstName: map[string]string{"10.0.0.5": "node-a.example"}})
	for range 2 {
		if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.5", false); err != nil {
			t.Fatalf("publish 10.0.0.5: %v", err)
		}
	}
}

// F2b: two spellings of one machine bypassed the mode conflict check, and
// nfs-utils keeps only the first matching line, so the second mode was
// never honored. Names are refused, and an overlapping network (the
// address form of a second spelling) is refused too.
func TestAdv4_TwoSpellingsBypassModeConflict(t *testing.T) {
	ctx := context.Background()
	ep := advSetup(t, nfsUtils{dns: map[string]string{"node-a": "10.0.0.5"}})
	if _, err := ep.PublishFileExport(ctx, "pvc", "node-a", false); endpoint.CodeOf(err) != endpoint.InvalidArgument {
		t.Fatalf("hostname: %v", err)
	}
	if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.5", false); err != nil {
		t.Fatal(err)
	}
	if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.5/32", true); endpoint.CodeOf(err) != endpoint.AlreadyExists {
		t.Fatalf("same address, other mode: %v", err)
	}
	if _, err := ep.PublishFileExport(ctx, "pvc", "10.0.0.0/24", true); endpoint.CodeOf(err) != endpoint.FailedPrecondition {
		t.Fatalf("overlapping network, other mode: %v", err)
	}
}

// F5: "*" was refused but 0.0.0.0/0 (every IPv4 host), two /1s, a /8 and
// inet_aton spellings were accepted. The default floor is /16.
func TestAdv4_ClientPolicyGaps(t *testing.T) {
	ctx := context.Background()
	ep := advSetup(t, nfsUtils{})
	for _, h := range []string{"0.0.0.0/0", "0.0.0.0/1", "128.0.0.0/1", "10.0.0.0/8", "10.0.0.0x5", "0x7f.0x1", "0xa000005"} {
		if _, err := ep.PublishFileExport(ctx, "pvc", h, false); endpoint.CodeOf(err) != endpoint.InvalidArgument {
			t.Errorf("%s: %v, want InvalidArgument", h, err)
		}
	}
}
