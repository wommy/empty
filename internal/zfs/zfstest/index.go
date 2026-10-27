package zfstest

import (
	"strconv"
	"strings"
)

// Index properties are stored by value, not by name: zfs_prop.c maps each
// accepted name to an index, and zprop_index_to_string prints the first
// name in the table with that index. So `-o compression=gzip-6` reads back
// as "gzip" and `-o xattr=on` as "sa". The simulator
// stores what real zfs would print, so a caller comparing read-backs
// against what it sent is tested against OpenZFS, not against an echo.

type indexName struct {
	name string
	val  int
}

// indexTables are zfs_prop.c's tables in table order (OpenZFS 2.4.0, the
// only supported userland). Only the properties the simulator models are
// listed.
var indexTables = func() map[string][]indexName {
	compress := []indexName{{"on", 1}, {"off", 2}, {"lzjb", 3}, {"gzip", 106}}
	for i := 1; i <= 9; i++ {
		compress = append(compress, indexName{"gzip-" + strconv.Itoa(i), 100 + i})
	}
	compress = append(compress, indexName{"zle", 4}, indexName{"lz4", 5}, indexName{"zstd", 6}, indexName{"zstd-fast", 1001})
	for i := 1; i <= 19; i++ {
		compress = append(compress, indexName{"zstd-" + strconv.Itoa(i), 200 + i})
	}
	for _, i := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 20, 30, 40, 50, 60, 70, 80, 90, 100, 500, 1000} {
		compress = append(compress, indexName{"zstd-fast-" + strconv.Itoa(i), 1000 + i})
	}
	onOff := []indexName{{"off", 0}, {"on", 1}}
	return map[string][]indexName{
		"compression": compress,
		"atime":       onOff,
		"relatime":    onOff,
		"dedup": {{"on", 1}, {"off", 0}, {"verify", 2}, {"sha256", 3}, {"sha256,verify", 4},
			{"sha512", 5}, {"sha512,verify", 6}, {"skein", 7}, {"skein,verify", 8},
			{"edonr,verify", 9}, {"blake3", 10}, {"blake3,verify", 11}},
		"sync":     {{"standard", 0}, {"always", 1}, {"disabled", 2}},
		"logbias":  {{"latency", 0}, {"throughput", 1}},
		"xattr":    {{"off", 0}, {"sa", 1}, {"on", 1}, {"dir", 2}},
		"acltype":  {{"off", 0}, {"posix", 1}, {"nfsv4", 2}, {"disabled", 0}, {"noacl", 0}, {"posixacl", 1}},
		"snapdir":  {{"hidden", 0}, {"visible", 1}, {"disabled", 2}},
		"copies":   {{"1", 1}, {"2", 2}, {"3", 3}},
		"canmount": {{"off", 0}, {"on", 1}, {"noauto", 2}},
		"exec":     onOff,
		"setuid":   onOff,
		"devices":  onOff,
	}
}()

// canonIndex returns what zfs get prints for value of an index property:
// the first name with its index. ok is false for a value the table lacks;
// msg is then libzfs's zprop_parse_value text.
func (s *Sim) canonIndex(prop, value string) (canon, msg string, ok bool) {
	t, isIndex := indexTables[prop]
	if !isIndex {
		return value, "", true
	}
	for _, e := range t {
		if e.name == value {
			for _, f := range t {
				if f.val == e.val {
					return f.name, "", true
				}
			}
		}
	}
	names := make([]string, len(t))
	for i, e := range t {
		names[i] = e.name
	}
	return "", "'" + prop + "' must be one of '" + strings.Join(names, " | ") + "'", false
}

// canonProps rewrites index values in props to what zfs get prints.
func (s *Sim) canonProps(props map[string]string) (string, bool) {
	for k, v := range props {
		c, msg, ok := s.canonIndex(k, v)
		if !ok {
			return msg, false
		}
		props[k] = c
	}
	return "", true
}
