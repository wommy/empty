package zfs

import (
	"context"
	"io"
	"log/slog"
	"time"
)

// mutating are the zfs subcommands that change pool state. Reads (get,
// list, version) are not audited.
var mutating = map[string]bool{
	"create": true, "destroy": true, "snapshot": true, "clone": true, "set": true,
	"inherit": true, "promote": true, "rename": true, "rollback": true,
	"share": true, "unshare": true, "mount": true, "unmount": true,
	"receive": true, "recv": true, "hold": true, "release": true,
}

// Audit returns a Runner that logs every state-changing command at Info,
// with its full argv, before it runs, and its outcome after. It logs with
// the call's context, so a handler that knows the request (the gRPC
// request id) can attach it: after an incident, "which call destroyed
// tank/k8s/pvc-x" and "which op did shutdown cut" are answered from the
// journal.
func Audit(r Runner, log *slog.Logger) Runner {
	if log == nil {
		log = slog.Default()
	}
	return auditor{r: r, log: log}
}

type auditor struct {
	r   Runner
	log *slog.Logger
}

func (a auditor) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out []byte
	err := a.audit(ctx, name, args, func() (err error) {
		out, err = a.r.Run(ctx, name, args...)
		return err
	})
	return out, err
}

func (a auditor) Stream(ctx context.Context, read func(io.Reader) error, name string, args ...string) error {
	return a.audit(ctx, name, args, func() error { return stream(ctx, a.r, read, name, args...) })
}

func (a auditor) audit(ctx context.Context, name string, args []string, run func() error) error {
	if len(args) == 0 || !mutating[args[0]] {
		return run()
	}
	argv := append([]string{name}, args...)
	a.log.InfoContext(ctx, "zfs exec", "argv", argv)
	start := time.Now()
	err := run()
	if err != nil {
		a.log.WarnContext(ctx, "zfs failed", "argv", argv, "duration", time.Since(start), "error", err)
	} else {
		a.log.InfoContext(ctx, "zfs done", "argv", argv, "duration", time.Since(start))
	}
	return err
}
