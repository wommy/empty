// Command unitprobe makes one call to a zfs-endpoint socket and says
// whether the answer is the one expected. ci/real-zfs-unit.sh runs it in
// the real-ZFS guest against the endpoint started by its systemd unit, as
// root and as uid 65534; it is a CI tool, not shipped.
//
// usage: unitprobe -addr unix:///path -want CODE op [arg]
//
//	health          gRPC health of the whole server: OK when SERVING
//	create <name>   CreateVolume, a 64 MiB filesystem; prints its id
//	delete <id>     DeleteVolume
//	reconcile       a dry-run Reconcile (the admin role's)
//
// CODE is a gRPC code name (OK, PermissionDenied, ...) or CONNECT: the
// socket refused the connection itself (its file mode, before any RPC).
// It prints "<op>: <code>[: detail]" and exits 0 when the code is CODE,
// 1 when it is not, 2 on a usage error.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	pb "github.com/wommy/empty/gen/zfsendpoint/v1"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}

// connectRefused is the -want name for a socket that refused connect(2).
const connectRefused = "CONNECT"

func run(args []string, out io.Writer) int {
	fs := flag.NewFlagSet("unitprobe", flag.ContinueOnError)
	fs.SetOutput(out)
	addr := fs.String("addr", "", "unix:///absolute/path of the socket")
	want := fs.String("want", "OK", "the gRPC code name expected, or "+connectRefused)
	timeout := fs.Duration("timeout", 10*time.Second, "deadline of the call")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	path, ok := strings.CutPrefix(*addr, "unix://")
	if !ok || fs.NArg() == 0 {
		fmt.Fprintln(out, "usage: unitprobe -addr unix:///path -want CODE health|create <name>|delete <id>|reconcile")
		return 2
	}
	op := fs.Arg(0)
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	got, detail := call(ctx, path, op, fs.Args()[1:])
	line := op + ": " + got
	if detail != "" {
		line += ": " + detail
	}
	fmt.Fprintln(out, line)
	if got != *want {
		fmt.Fprintf(out, "%s: want %s\n", op, *want)
		return 1
	}
	return 0
}

// call makes the call and returns the code's name and a detail: the id a
// create made, or the error's message.
func call(ctx context.Context, path, op string, args []string) (string, string) {
	// connect first, apart from gRPC, which would retry a refused connect
	// until the deadline and report it only as Unavailable
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return connectRefused, err.Error()
	}
	c.Close()
	conn, err := grpc.NewClient("unix://"+path, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return codes.Internal.String(), err.Error()
	}
	defer conn.Close()
	ep := pb.NewEndpointServiceClient(conn)
	arg := func() string {
		if len(args) == 0 {
			return ""
		}
		return args[0]
	}
	detail := ""
	switch op {
	case "health":
		var r *healthpb.HealthCheckResponse
		if r, err = healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{}); err == nil &&
			r.GetStatus() != healthpb.HealthCheckResponse_SERVING {
			return "NOT_SERVING", r.GetStatus().String()
		}
	case "create":
		var r *pb.CreateVolumeResponse
		r, err = ep.CreateVolume(ctx, &pb.CreateVolumeRequest{Name: arg(), Kind: pb.VolumeKind_VOLUME_KIND_FILESYSTEM, CapacityBytes: 64 << 20})
		detail = r.GetVolume().GetId()
	case "delete":
		_, err = ep.DeleteVolume(ctx, &pb.DeleteVolumeRequest{VolumeId: arg()})
	case "reconcile":
		_, err = ep.Reconcile(ctx, &pb.ReconcileRequest{DryRun: true})
	default:
		return "USAGE", "unknown op " + op
	}
	if err != nil {
		s := status.Convert(err)
		return s.Code().String(), s.Message()
	}
	return codes.OK.String(), detail
}
