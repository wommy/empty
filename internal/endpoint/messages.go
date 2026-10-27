package endpoint

import (
	"fmt"
	"time"
)

// pending is the tail of a lock refusal's message: how long the call
// waited, and who holds the first busy key and for how long (lock.Holder;
// the owner is the holder's request id, which its rpc and zfs lines
// carry). Best effort: the holder may have released it since.
func (e *Endpoint) pending(keys []string, waited time.Duration) string {
	for _, k := range keys {
		owner, since, ok := e.locks.Holder(k)
		if !ok {
			continue
		}
		by := ""
		if owner != "" {
			by = " by request " + owner
		}
		return fmt.Sprintf(" (waited %s; %s held for %s%s)", roundDur(waited), k, roundDur(time.Since(since)), by)
	}
	return fmt.Sprintf(" (waited %s)", roundDur(waited))
}

// roundDur is d to the second, or to the millisecond under one.
func roundDur(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(time.Second)
}
