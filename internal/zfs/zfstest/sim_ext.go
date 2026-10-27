package zfstest

import (
	"strconv"
	"strings"

	"github.com/wommy/empty/internal/zfs"
)

// Out-of-band administration the endpoint never runs but meets on a real
// pool: `zfs rename`, and the guid that identifies a dataset across it.
// Kept apart from sim.go so the core command set stays the endpoint's.

// guid is a dataset's guid: handed out on first read, kept for life.
func (s *Sim) guid(n *node) string {
	if n.guid == 0 {
		s.guids++
		n.guid = 1000 + s.guids
	}
	return strconv.FormatUint(n.guid, 10)
}

// rename is `zfs rename a b` for a filesystem or zvol in the same pool:
// descendants and snapshots move with it, and guids, properties, holds and
// the clone/origin graph follow. A shared filesystem is unshared under its
// old mountpoint and reshared under the new one, as the changelist does.
func (s *Sim) rename(args []string) error {
	if len(args) != 2 {
		return exit("missing source dataset argument")
	}
	from, to := args[0], args[1]
	n, ok := s.ds[from]
	if !ok {
		return exit("cannot open '%s': dataset does not exist", from)
	}
	if n.typ == zfs.TypeSnapshot || strings.Contains(to, "@") {
		return exit("cannot rename to '%s': snapshot rename is not modelled", to)
	}
	if _, ok := s.ds[to]; ok {
		return exit("cannot rename to '%s': dataset already exists", to)
	}
	p, ok := s.ds[parent(to)]
	if !ok {
		return exit("cannot rename to '%s': parent does not exist", to)
	}
	if p.typ != zfs.TypeFilesystem {
		return exit("cannot rename to '%s': parent is not a filesystem", to)
	}
	if strings.HasPrefix(to, from+"/") {
		return exit("cannot rename to '%s': New name is a descendant of current name", to)
	}
	moved := map[string]string{from: to}
	for _, k := range s.children(from) {
		moved[k] = to + k[len(from):]
	}
	changed := false
	for old := range moved {
		changed = s.unshare(old) || changed
	}
	if changed {
		s.exportfs()
	}
	nodes := map[string]*node{}
	for old, nw := range moved {
		nodes[nw] = s.ds[old]
		delete(s.ds, old)
	}
	for nw, d := range nodes {
		s.ds[nw] = d
	}
	for _, d := range s.ds {
		if nw, ok := moved[d.origin]; ok {
			d.origin = nw
		}
	}
	s.reshare(to)
	return nil
}
