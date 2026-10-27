package endpoint_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/wommy/empty/internal/endpoint"
)

// rpcCallBudget pins the zfs calls each RPC's main path makes on the
// simulator, in order: every zfs(8) call is a fork on a real pool, and
// forks are the endpoint's cost (ADR 0005). A change that adds or drops a
// call shows up here. When that change is intended, replace this table
// with the one the failure prints.
//
// Spelling (budgetCall): the command's arguments with -j/-p dropped, each
// -o pair dropped, set values dropped (keys kept), the scan's column list
// as <cols>, the parent as P and the property prefix as "ns:".
var rpcCallBudget = []rpcBudget{
	{"CreateVolume block new", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/b",
		"get type P",
		"create -V 1073741824 P/b",
		"set ns:committed_at ns:provision_success P/b",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/b",
	}},
	{"CreateVolume block retry", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/b",
	}},
	{"CreateVolume fs new", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
		"get type P",
		"get mountpoint P",
		"create P/f",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
		"set ns:committed_at ns:provision_success P/f",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
	}},
	{"CreateVolume fs retry", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
	}},
	{"CreateVolume nfs new", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/n",
		"get type P",
		"get mountpoint P",
		"create P/n",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/n",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/n",
		"set ns:committed_at ns:provision_success P/n",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/n",
	}},
	{"CreateVolume nfs retry", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/n",
	}},
	{"CreateSnapshot new", []string{
		"get -t filesystem,volume,snapshot -d 2 <cols> P",
		"snapshot P/b@s",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/b",
	}},
	{"CreateSnapshot retry", []string{
		"get -t filesystem,volume,snapshot -d 2 <cols> P",
	}},
	{"CreateVolume clone of snapshot new", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/c",
		"get type P",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/b",
		"clone P/b@s P/c",
		"set refreservation P/c",
		"set ns:committed_at ns:provision_success P/c",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/c",
	}},
	{"CreateVolume clone of snapshot retry", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/c",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/b",
	}},
	{"CreateVolume fs clone of volume new", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/cf",
		"get type P",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
		"get mountpoint P",
		"snapshot P/f@volume-source-for-volume-cf",
		"clone P/f@volume-source-for-volume-cf P/cf",
		"set refquota P/cf",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/cf",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
		"set ns:committed_at ns:provision_success P/cf",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/cf",
	}},
	{"CreateVolume fs clone of volume retry", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/cf",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
	}},
	{"ExpandVolume new", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
		"set refquota P/f",
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
	}},
	{"ExpandVolume retry", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/f",
	}},
	{"DeleteVolume present", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/d",
		"get -t snapshot -d 2 type P",
		"get guid P/d",
		"destroy P/d",
	}},
	{"DeleteVolume absent", []string{
		"get -t filesystem,volume,snapshot -d 1 <cols> P/d",
		"get -t snapshot -d 2 type P",
	}},
}

type rpcBudget struct {
	rpc   string
	calls []string
}

// TestRPCCallBudget runs each main RPC path once on one simulated
// endpoint and compares the zfs calls it made with rpcCallBudget.
// Simulator only: real zfs paths may differ (a settle, a retried busy
// destroy), and the budget is the endpoint's, not the pool's.
func TestRPCCallBudget(t *testing.T) {
	t.Parallel()
	requireSim(t)
	ctx := context.Background()
	_, h := setupNFS(t, "192.0.2.10")
	r, take := endpoint.RecordCalls(h.Z.Runner)
	ep := crashEndpoint(t, h, r, false)
	const gib = 1 << 30

	var got []rpcBudget
	step := func(rpc string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", rpc, err)
		}
		var calls []string
		for _, c := range take() {
			calls = append(calls, budgetCall(h.Parent, c))
		}
		got = append(got, rpcBudget{rpc, calls})
	}
	create := func(rpc string, req endpoint.CreateVolumeRequest) {
		t.Helper()
		_, err := ep.CreateVolume(ctx, req)
		step(rpc, err)
	}
	block := endpoint.CreateVolumeRequest{Name: "b", Kind: endpoint.Block, CapacityBytes: gib}
	fs := endpoint.CreateVolumeRequest{Name: "f", CapacityBytes: gib}
	nfs := endpoint.CreateVolumeRequest{Name: "n", CapacityBytes: gib, NFS: &endpoint.NFSExport{Clients: []string{"10.20.0.0/24"}}}
	create("CreateVolume block new", block)
	create("CreateVolume block retry", block)
	create("CreateVolume fs new", fs)
	create("CreateVolume fs retry", fs)
	create("CreateVolume nfs new", nfs)
	create("CreateVolume nfs retry", nfs)
	snap, err := ep.CreateSnapshot(ctx, "s", "b")
	step("CreateSnapshot new", err)
	_, err = ep.CreateSnapshot(ctx, "s", "b")
	step("CreateSnapshot retry", err)
	clone := endpoint.CreateVolumeRequest{Name: "c", Kind: endpoint.Block, CapacityBytes: gib, SourceSnapshotID: snap.ID}
	create("CreateVolume clone of snapshot new", clone)
	create("CreateVolume clone of snapshot retry", clone)
	fsClone := endpoint.CreateVolumeRequest{Name: "cf", CapacityBytes: gib, SourceVolumeID: "f"}
	create("CreateVolume fs clone of volume new", fsClone)
	create("CreateVolume fs clone of volume retry", fsClone)
	_, err = ep.ExpandVolume(ctx, "f", 2*gib, 0)
	step("ExpandVolume new", err)
	_, err = ep.ExpandVolume(ctx, "f", 2*gib, 0)
	step("ExpandVolume retry", err)
	if _, err := ep.CreateVolume(ctx, endpoint.CreateVolumeRequest{Name: "d", Kind: endpoint.Block, CapacityBytes: gib}); err != nil {
		t.Fatal(err)
	}
	take()
	step("DeleteVolume present", ep.DeleteVolume(ctx, "d"))
	step("DeleteVolume absent", ep.DeleteVolume(ctx, "d"))

	if !slices.EqualFunc(got, rpcCallBudget, func(a, b rpcBudget) bool {
		return a.rpc == b.rpc && slices.Equal(a.calls, b.calls)
	}) {
		var diff strings.Builder
		for i, g := range got {
			var want []string
			if i < len(rpcCallBudget) {
				want = rpcCallBudget[i].calls
			}
			if !slices.Equal(g.calls, want) {
				fmt.Fprintf(&diff, "%s: %d calls, budget %d\n", g.rpc, len(g.calls), len(want))
			}
		}
		t.Errorf("the RPCs' zfs calls differ from rpcCallBudget:\n%s\n"+
			"If the change is intended, replace rpcCallBudget in callbudget_test.go with:\n\n%s",
			diff.String(), budgetSource(got))
	}
}

// budgetCall spells one zfs call as rpcCallBudget does.
func budgetCall(parent string, args []string) string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-j" || a == "-p":
			continue
		case a == "-o" && i+1 < len(args):
			i++ // values are the plan's (golden_plan_test.go), not the budget's
			continue
		case args[0] == "get" && strings.Count(a, ",") >= 8:
			a = "<cols>"
		case args[0] == "set" && strings.Contains(a, "="):
			a, _, _ = strings.Cut(a, "=")
		}
		a = strings.ReplaceAll(a, endpoint.DefaultPropertyPrefix+":", "ns:")
		if a == parent || strings.HasPrefix(a, parent+"/") || strings.HasPrefix(a, parent+"@") {
			a = "P" + a[len(parent):]
		}
		out = append(out, a)
	}
	if len(out) > 0 && out[0] == "set" {
		// a set's keys come in map order: sort them
		keys := out[1 : len(out)-1]
		slices.Sort(keys)
	}
	return strings.Join(out, " ")
}

// budgetSource prints got as the Go source of rpcCallBudget.
func budgetSource(got []rpcBudget) string {
	var b strings.Builder
	b.WriteString("var rpcCallBudget = []rpcBudget{\n")
	for _, g := range got {
		fmt.Fprintf(&b, "\t{%q, []string{\n", g.rpc)
		for _, c := range g.calls {
			fmt.Fprintf(&b, "\t\t%q,\n", c)
		}
		b.WriteString("\t}},\n")
	}
	b.WriteString("}\n")
	return b.String()
}
