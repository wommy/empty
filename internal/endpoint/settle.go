package endpoint

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"time"
)

// maxSettle bounds one UdevSettle when the caller has no deadline:
// udevadm's own default is 120s.
const maxSettle = 10 * time.Second

// UdevSettle is Config.Settle for a host running systemd-udevd: `udevadm
// settle`, which returns once udevd has handled every event already raised
// (it pings udevd, so an event the kernel sent before the call is
// included), bounded by ctx's deadline and maxSettle. A host without
// udevadm has no udev rules to wait for: nil.
func UdevSettle(ctx context.Context) error {
	timeout := maxSettle
	if dl, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(dl))
	}
	secs := max(int(timeout/time.Second), 1)
	err := exec.CommandContext(ctx, "udevadm", "settle", "--timeout="+strconv.Itoa(secs)).Run()
	if errors.Is(err, exec.ErrNotFound) {
		return nil
	}
	return err
}
