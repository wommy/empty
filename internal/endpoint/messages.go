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

// notManaged says why row, a dataset at a volume's name, is not the
// endpoint's volume (Row.OwnedVolume), and what an operator can do: the
// markers must be set locally and name the volume (ADR 0003).
func (p Planner) notManaged(row Row) string {
	m, n := p.Names.managed(), p.Names.volumeName()
	var why string
	switch {
	case !row.Managed && row.ManagedSource == "":
		why = "it has no " + m + " marker"
	case !row.Managed && row.ManagedSource == "received":
		why = "its " + m + " is received (zfs recv), not set locally: a copy of a volume is not the volume"
	case !row.Managed && row.ManagedSource == "local":
		why = "its " + m + " is set locally, but not to true"
	case !row.Managed:
		why = "its " + m + " is " + row.ManagedSource + ", not set on it"
	case row.VolumeName == "":
		why = "it has " + m + " but no local " + n
	default:
		why = fmt.Sprintf("its %s is %q, not %q: renamed, or copied from another volume", n, row.VolumeName, row.ID)
	}
	return why + "; if it is this volume, set both markers locally (zfs set " + m + "=true " + n + "=" + row.ID + "), otherwise rename it away"
}

// uncommittedClone is the tail of a "has dependent clone" refusal when
// the clone is an uncommitted create: an aborted CreateVolume from this
// volume, which a Reconcile collects, after which the delete goes
// through. DeleteVolume's table may not hold the clone (it reads only the
// volume and the sources stamped for it): then the clone-source stamp
// says only that the clone was made from this volume, and the tail is a
// hint.
func uncommittedClone(t Table, s Row, clone string) string {
	const collects = "zfs-endpoint reconcile collects it once older than -min-age, and this delete then goes through"
	if r, ok := t.Get(clone); ok {
		if r.OwnedVolume() && !r.Committed {
			return " (" + clone + " is an uncommitted create, never provisioned: " + collects + ")"
		}
		return ""
	}
	if c, ours := s.OwnedCloneSource(); ours && c == clone {
		return " (if " + clone + "'s CreateVolume never completed, " + collects + ")"
	}
	return ""
}

// roundDur is d to the second, or to the millisecond under one.
func roundDur(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(time.Second)
}
