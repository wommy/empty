package zfstest

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// JSONDataset is one dataset of `zfs get -j -p` output.
type JSONDataset struct {
	Name      string
	Type      string // filesystem, volume, snapshot
	Createtxg uint64
	Props     []JSONProp // in request order
}

// JSONProp is one property; Source is in -Hp spelling (local, default,
// inherited from X, received, temporary, -).
type JSONProp struct{ Name, Value, Source string }

// AppendGetJSON renders datasets the way OpenZFS 2.4 `zfs get -j -p` does
// under LC_ALL=C.UTF-8: zfs_json_schema(0, 1), fill_dataset_info and
// zprop_nvlist_one_property build the nvlist, nvlist_print_json prints it
// with no whitespace, and zcmd_print_json adds a newline.
//
// Faithful to libnvpair_json.c, including its faults: a string with invalid
// UTF-8 stops the printer mid-document (zcmd_print_json ignores the error,
// still prints the newline, and zfs exits 0), and code points above U+FFFF
// are silently dropped. A dataset with no properties is left out
// (get_callback adds it only if its property nvlist is non-empty).
func AppendGetJSON(dst []byte, ds []JSONDataset) []byte {
	w := jsonw{b: dst}
	w.raw(`{"output_version":{"command":"zfs get","vers_major":0,"vers_minor":1},"datasets":{`)
	first := true
	for _, d := range ds {
		if len(d.Props) == 0 {
			continue
		}
		if !first {
			w.raw(",")
		}
		first = false
		w.str(d.Name)
		w.raw(`:{"name":`)
		w.str(d.Name)
		w.raw(`,"type":`)
		w.str(strings.ToUpper(d.Type))
		dsName, snap, isSnap := strings.Cut(d.Name, "@")
		pool, _, _ := strings.Cut(dsName, "/")
		w.raw(`,"pool":`)
		w.str(pool)
		w.raw(`,"createtxg":`)
		w.str(strconv.FormatUint(d.Createtxg, 10))
		if isSnap {
			w.raw(`,"dataset":`)
			w.str(dsName)
			w.raw(`,"snapshot_name":`)
			w.str(snap)
		}
		w.raw(`,"properties":{`)
		for i, p := range d.Props {
			if i > 0 {
				w.raw(",")
			}
			typ, data := jsonSource(p.Source)
			w.str(p.Name)
			w.raw(`:{"value":`)
			w.str(p.Value)
			w.raw(`,"source":{"type":`)
			w.str(typ)
			w.raw(`,"data":`)
			w.str(data)
			w.raw("}}")
		}
		w.raw("}}")
	}
	w.raw("}}")
	return append(w.b, '\n')
}

func jsonSource(text string) (typ, data string) {
	switch text {
	case "-":
		return "NONE", "-"
	case "default", "local", "temporary", "received":
		return strings.ToUpper(text), "-"
	}
	if from, ok := strings.CutPrefix(text, "inherited from "); ok {
		return "INHERITED", from
	}
	panic("zfstest: unknown source " + strconv.Quote(text))
}

// jsonw is nvlist_print_json's FPRINTF chain: after the first failure
// nothing more is written.
type jsonw struct {
	b      []byte
	failed bool
}

func (w *jsonw) raw(s string) {
	if !w.failed {
		w.b = append(w.b, s...)
	}
}

// str is nvlist_print_json_string with mbrtowc in a UTF-8 locale.
func (w *jsonw) str(s string) {
	if w.failed {
		return
	}
	w.b = append(w.b, '"')
	for len(s) > 0 {
		c, sz := utf8.DecodeRuneInString(s)
		if c == utf8.RuneError && sz <= 1 {
			w.failed = true // mbrtowc: (size_t)-1 or -2
			return
		}
		switch c {
		case '"':
			w.b = append(w.b, `\"`...)
		case '\n':
			w.b = append(w.b, `\n`...)
		case '\r':
			w.b = append(w.b, `\r`...)
		case '\\':
			w.b = append(w.b, `\\`...)
		case '\f':
			w.b = append(w.b, `\f`...)
		case '\t':
			w.b = append(w.b, `\t`...)
		case '\b':
			w.b = append(w.b, `\b`...)
		default:
			switch {
			case c <= 0x1f || (c > 0x7f && c <= 0xffff):
				w.b = append(w.b, `\u`...)
				h := strconv.FormatInt(int64(c), 16) // "%04x": lower case
				w.b = append(w.b, "000"[:max(0, 4-len(h))]...)
				w.b = append(w.b, h...)
			case c <= 0x7f:
				w.b = append(w.b, byte(c))
			}
			// above U+FFFF: dropped
		}
		s = s[sz:]
	}
	w.b = append(w.b, '"')
}
