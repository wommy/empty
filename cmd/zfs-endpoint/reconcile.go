package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
	"github.com/wommy/empty/internal/endpoint"
	"github.com/wommy/empty/internal/server"
	"github.com/wommy/empty/internal/unixsock"
)

// reconcileUsage heads `zfs-endpoint reconcile -h`.
const reconcileUsage = `usage: zfs-endpoint reconcile -parent <dataset> [flags]

Asks a running endpoint, on its admin socket, what Reconcile would collect,
and prints the plan and its plan_digest. Nothing is changed unless -apply
is given with -digest, the plan_digest of a dry run you looked at (ADR 0001
D3): the endpoint then applies only if it still plans exactly that.

The desired set is the complete list of volume ids to keep (-desired,
-desired-file). Empty, it collects no committed volume: only uncommitted
rows (abandoned creates) and orphan clone-source snapshots older than
-min-age, and it reports drift in "skipped".
`

// runReconcile is `zfs-endpoint reconcile`: a client of the admin socket.
func runReconcile(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("zfs-endpoint reconcile", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), reconcileUsage+"\nflags:\n")
		fs.PrintDefaults()
	}
	parent := fs.String("parent", "", "parent dataset the endpoint serves; picks its admin socket, "+unixsock.EndpointAdminAddr("<parent>"))
	addr := fs.String("admin-socket", "", "unix:///absolute/path of the endpoint's admin socket (-admin-listen); default from -parent")
	var desired []string
	fs.Func("desired", "a volume id to keep; repeat for more", func(s string) error {
		desired = append(desired, s)
		return nil
	})
	desiredFile := fs.String("desired-file", "", "file of volume ids to keep, one per line (blank lines and # comments skipped); - reads stdin")
	minAge := fs.Duration("min-age", 0, "touch nothing created or committed less than this long ago; 0: the endpoint's default (1h); at least 10m")
	maxDel := fs.Uint("max-deletions", 0, "most committed volumes one run may collect as undesired; 0: the endpoint's default (10)")
	unlimited := fs.Bool("unlimited-deletions", false, "lift -max-deletions")
	apply := fs.Bool("apply", false, "apply the plan (default: dry run only); needs -digest")
	digest := fs.String("digest", "", "plan_digest of the dry run being applied")
	verbose := fs.Bool("v", false, "list every skipped volume, including the committed ones an empty desired set does not judge")
	timeout := fs.Duration("timeout", 5*time.Minute, "deadline for the call")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return configErr("reconcile: %v", err)
	}
	if fs.NArg() > 0 {
		return configErr("reconcile: unexpected arguments %q", fs.Args())
	}
	if *addr == "" {
		if *parent == "" {
			return configErr("reconcile: -parent or -admin-socket is required")
		}
		*addr = unixsock.EndpointAdminAddr(*parent)
	}
	path, ok := strings.CutPrefix(*addr, "unix://")
	if !ok {
		return configErr("reconcile: -admin-socket %q: want unix:///absolute/path", *addr)
	}
	if err := unixsock.CheckPath(path); err != nil {
		return configErr("reconcile: -admin-socket: %v", err)
	}
	if *apply && *digest == "" {
		return configErr("reconcile: -apply needs -digest, the plan_digest of a dry run you looked at (run without -apply first)")
	}
	if !*apply && *digest != "" {
		return configErr("reconcile: -digest is only for -apply")
	}
	if *minAge < 0 || *minAge%time.Second != 0 {
		return configErr("reconcile: -min-age %v: want whole seconds", *minAge)
	}
	if *maxDel > 0 && *unlimited {
		return configErr("reconcile: -max-deletions and -unlimited-deletions together")
	}
	if *desiredFile != "" {
		ids, err := readDesired(*desiredFile)
		if err != nil {
			return configErr("reconcile: -desired-file: %v", err)
		}
		desired = append(desired, ids...)
	}

	conn, err := grpc.NewClient("unix://"+path,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(server.MaxMsgSize), grpc.MaxCallSendMsgSize(server.MaxMsgSize)))
	if err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	resp, err := pb.NewEndpointServiceClient(conn).Reconcile(ctx, &pb.ReconcileRequest{
		DesiredVolumeIds:   desired,
		MinAgeSeconds:      uint64(*minAge / time.Second),
		DryRun:             !*apply,
		MaxDeletions:       uint32(*maxDel),
		UnlimitedDeletions: *unlimited,
		PlanDigest:         *digest,
	})
	if err != nil {
		return fmt.Errorf("reconcile on %s: %w", path, err)
	}
	printPlan(out, resp, *apply, *verbose)
	return nil
}

// readDesired reads volume ids, one per line; "-" is stdin.
func readDesired(name string) ([]string, error) {
	var r io.Reader = os.Stdin
	if name != "-" {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}
	var ids []string
	sc := bufio.NewScanner(r)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if id := strings.TrimSpace(sc.Text()); id != "" && !strings.HasPrefix(id, "#") {
			ids = append(ids, id)
		}
	}
	return ids, sc.Err()
}

// printPlan writes the plan (or, applied, what ran) and its digest. The
// committed volumes an empty desired set leaves unjudged are counted, not
// listed, unless verbose: on a report they say nothing but "the set was
// empty".
func printPlan(w io.Writer, r *pb.ReconcileResponse, applied, verbose bool) {
	verb := "planned (dry run, nothing changed)"
	if applied {
		verb = "applied"
	}
	fmt.Fprintf(w, "plan_digest: %s\n", r.GetPlanDigest())
	fmt.Fprintf(w, "actions %s: %d\n", verb, len(r.GetActions()))
	for _, a := range r.GetActions() {
		fmt.Fprintf(w, "  %s %s: %s\n", a.GetOp(), a.GetTarget(), a.GetReason())
	}
	unjudged := 0
	var listed []*pb.ReconcileSkip
	for _, s := range r.GetSkipped() {
		if s.GetReason() == endpoint.SkipEmptyDesired && !verbose {
			unjudged++
			continue
		}
		listed = append(listed, s)
	}
	fmt.Fprintf(w, "skipped: %d\n", len(r.GetSkipped()))
	for _, s := range listed {
		fmt.Fprintf(w, "  %s: %s\n", s.GetTarget(), s.GetReason())
	}
	if unjudged > 0 {
		fmt.Fprintf(w, "  (%d committed volumes: %s; -v lists them)\n", unjudged, endpoint.SkipEmptyDesired)
	}
	if !applied && len(r.GetActions()) > 0 {
		fmt.Fprintf(w, "to apply exactly this plan: zfs-endpoint reconcile <the same flags> -apply -digest %s\n", r.GetPlanDigest())
	}
}
