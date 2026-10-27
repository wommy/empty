package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/wommy/empty/internal/zfs"
)

// Exit codes (sysexits.h), for the unit's restart policy:
//   - exitConfig: bad flags, wrong OpenZFS, a parent that is not a
//     filesystem. Restarting cannot help; RestartPreventExitStatus=78.
//   - exitTempFail: the parent is not there yet (pool not imported, key
//     not loaded). Restart=on-failure retries it with backoff.
//   - 1: anything else.
const (
	exitTempFail = 75 // EX_TEMPFAIL
	exitConfig   = 78 // EX_CONFIG
)

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func configErr(format string, a ...any) error {
	return &exitError{exitConfig, fmt.Errorf(format, a...)}
}

func tempErr(format string, a ...any) error {
	return &exitError{exitTempFail, fmt.Errorf(format, a...)}
}

func exitCode(err error) int {
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}

// cannotRun reports a zfs binary that is missing from PATH or at its path:
// configuration, not a transient failure.
func cannotRun(err error) bool {
	return errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist)
}

// ZFSSeries is the only supported OpenZFS release series (ADR 0004).
const ZFSSeries = "2.4"

// checkVersion refuses to serve unless both the zfs userland and the
// loaded kernel module are OpenZFS 2.4: older releases print other
// spellings and take parse paths ADR 0004 dropped, and a mixed pair (a
// 2.2 userland on a 2.3 module) is not tested anywhere.
func checkVersion(ctx context.Context, z *zfs.Client) error {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	v, err := z.Version(ctx)
	switch {
	case cannotRun(err):
		return configErr("cannot run zfs: %v", err)
	case err != nil:
		// e.g. the module is not loaded yet at boot
		return tempErr("cannot read the OpenZFS version: %v", err)
	}
	slog.Info("openzfs", "userland", v.Userland, "kernel", v.Kernel)
	if err := v.Require(ZFSSeries); err != nil {
		return configErr("%v (ADR 0004)", err)
	}
	return nil
}

// validParent accepts a dataset name zfs would, minus "." and ".."
// components: "tank/../x" passes the name pattern but is not a dataset,
// and would name the lock file of another.
func validParent(p string) error {
	if !zfs.ValidDataset(p) {
		return configErr("invalid parent dataset %q", p)
	}
	for _, c := range strings.Split(p, "/") {
		if c == "." || c == ".." {
			return configErr("invalid parent dataset %q: %q component", p, c)
		}
	}
	return nil
}

// waitParent waits for the parent to exist as a filesystem.
//
// At boot the pool may not be imported yet (slow disks, an encrypted pool
// waiting for its key). The endpoint retries in process, with backoff
// from 1s to 10s, for up to wait: that covers the common short race
// without a restart. Past that it exits 75 and the unit's
// Restart=on-failure (with StartLimitIntervalSec=0 and RestartSteps
// backoff) keeps trying, however long the import takes, and each attempt
// shows in the journal. Serving meanwhile would buy nothing: every call
// would be Unavailable, which is what front ends see while the socket is
// absent.
//
// A parent that exists but is a volume (or anything not a filesystem) is
// a configuration error. Each probe is bounded: a suspended pool blocks
// zfs indefinitely.
func waitParent(ctx context.Context, z *zfs.Client, parent string, wait time.Duration, after func(time.Duration) <-chan time.Time) error {
	deadline := time.Now().Add(wait)
	backoff := time.Second
	for {
		pctx, cancel := context.WithTimeout(ctx, probeTimeout)
		ds, err := z.Get(pctx, parent, "type")
		cancel()
		switch {
		case err == nil && ds.Type() == zfs.TypeFilesystem:
			return nil
		case err == nil:
			return configErr("parent dataset %s is a %s, not a filesystem", parent, ds.Type())
		case ctx.Err() != nil:
			return ctx.Err()
		case cannotRun(err):
			return configErr("cannot run zfs: %v", err)
		}
		if time.Now().Add(backoff).After(deadline) {
			return tempErr("parent dataset %s is not available after %v: %v", parent, wait, err)
		}
		slog.Warn("waiting for the parent dataset", "parent", parent, "retry_in", backoff, "error", err)
		notify("STATUS=waiting for " + parent)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-after(backoff):
		}
		backoff = min(2*backoff, 10*time.Second)
	}
}

// legacyPrefix is the property namespace builds before 7f5cb39 wrote by
// default; -property-prefix democratic-csi still adopts its volumes.
const legacyPrefix = "democratic-csi"

// checkLegacyPrefix refuses (78) to serve a parent whose volumes all carry
// legacyPrefix's managed marker and none carry prefix's: an upgrade across
// the default's change, without the flag. Served, every such volume reads
// as not the endpoint's: ListVolumes is empty, DeleteVolume is
// FAILED_PRECONDITION and a CreateVolume retry ALREADY_EXISTS. That fails
// safe, but it is an outage nobody asked for.
//
// It is one `zfs get -d 1` of the two markers on the parent's children
// (volumes are direct children), bounded like every startup probe. Only
// local values count, as for ownership (ADR 0003). A parent with no
// volumes, or with any of prefix's, passes.
func checkLegacyPrefix(ctx context.Context, z *zfs.Client, parent, prefix string) error {
	if prefix == legacyPrefix {
		return nil
	}
	ours, old := prefix+":managed_resource", legacyPrefix+":managed_resource"
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	children, err := z.List(ctx, parent, zfs.ListOptions{Props: []string{ours, old}})
	switch {
	case cannotRun(err):
		return configErr("cannot run zfs: %v", err)
	case err != nil:
		return tempErr("scan %s for volumes under another property prefix: %v", parent, err)
	}
	legacy := 0
	for _, c := range children {
		if marked(c, ours) {
			return nil
		}
		if marked(c, old) {
			legacy++
		}
	}
	if legacy > 0 {
		return configErr("%d datasets under %s carry %s and none carry %s: they were provisioned under the old default prefix; "+
			"pass -property-prefix %s to keep serving them, or -property-prefix %s to start a new namespace beside them",
			legacy, parent, old, ours, legacyPrefix, prefix)
	}
	return nil
}

// marked reports a locally set "true" marker.
func marked(d zfs.Dataset, prop string) bool {
	p, ok := d.Props[prop]
	return ok && p.Local() && p.Value == "true"
}
