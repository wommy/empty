package nfsref

import (
	"fmt"
	"strconv"
	"strings"
)

// Export flags (nfs-utils support/include/nfs/export.h:14-30).
const (
	ExpReadonly       = 0x0001
	ExpInsecurePort   = 0x0002
	ExpRootSquash     = 0x0004
	ExpAllSquash      = 0x0008
	ExpAsync          = 0x0010
	ExpGatheredWrites = 0x0020
	ExpNoReaddirPlus  = 0x0040
	ExpSecurityLabel  = 0x0080
	ExpSignFH         = 0x0100
	ExpNoHide         = 0x0200
	ExpNoSubtreeCheck = 0x0400
	ExpNoAuthNLM      = 0x0800
	ExpFSID           = 0x2000
	ExpCrossMount     = 0x4000
	ExpNoACL          = 0x8000
	ExpV4Root         = 0x10000
	ExpPNFS           = 0x20000
)

// exportDefaultFlags is EXPORT_DEFAULT_FLAGS (exports.c:37-38).
const exportDefaultFlags = ExpReadonly | ExpRootSquash | ExpGatheredWrites | ExpNoSubtreeCheck

// SecinfoFlags is what the kernel lets vary per security flavor, read by
// get_export_features from /proc/fs/nfsd/export_features (exports.c:965).
// Kernels since 3.x report NFSEXP_SECINFO_FLAGS: ro, insecure, root_squash,
// all_squash (ambiguity: kernel; the replay reads the real file).
var SecinfoFlags = ExpReadonly | ExpInsecurePort | ExpRootSquash | ExpAllSquash

const secflavorCount = 8 // nfslib.h:59

// flavor is flav_map (exports.c:40-48): name and RPC flavor number.
type flavor struct {
	name string
	num  int
}

var flavors = []flavor{
	{"krb5", 390003}, {"krb5i", 390004}, {"krb5p", 390005},
	{"unix", 1}, {"sys", 1}, {"null", 0}, {"none", 0},
}

func findFlavor(name string) *flavor {
	for i := range flavors {
		if flavors[i].name == name {
			return &flavors[i]
		}
	}
	return nil
}

// SecEntry is one e_secinfo slot: a flavor and the flags it is exported with.
type SecEntry struct {
	Flavor string
	Num    int
	Flags  int
}

// Entry is struct exportent, the fields the export path decides on.
type Entry struct {
	Path      string
	Hostname  string
	Flags     int
	AnonUID   int64
	AnonGID   int64
	Secinfo   []SecEntry
	Xprtsec   []string
	Mountpt   *string // e_mountpoint: nil unset, "" for bare "mountpoint"
	FsidNum   uint64
	UUID      string
	FslocKind string // "", "refer", "replicas"
	FslocData string
	Reexport  string
	Squids    []int
	Sqgids    []int
}

func (e Entry) clone() Entry {
	e.Secinfo = append([]SecEntry(nil), e.Secinfo...)
	e.Xprtsec = append([]string(nil), e.Xprtsec...)
	e.Squids = append([]int(nil), e.Squids...)
	e.Sqgids = append([]int(nil), e.Sqgids...)
	if e.Mountpt != nil {
		m := *e.Mountpt
		e.Mountpt = &m
	}
	return e
}

// initExportent is init_exportent (exports.c:84-110) with fromkernel=0.
func initExportent() Entry {
	return Entry{Flags: exportDefaultFlags, AnonUID: 65534, AnonGID: 65534}
}

// ---- xio.c: the tokenizer ----------------------------------------------------

// xfile is XFILE over an in-memory file (support/nfs/xio.c).
type xfile struct {
	b    []byte
	pos  int
	line int
}

const eof = -1

func (x *xfile) getc() int {
	if x.pos >= len(x.b) {
		x.pos = len(x.b) + 1 // past EOF; ungetc(EOF) is a no-op
		return eof
	}
	c := int(x.b[x.pos])
	x.pos++
	return c
}

func (x *xfile) ungetcRaw(c int) {
	if c == eof {
		return
	}
	x.pos--
}

// xgetc (xio.c): backslash-newline joins lines and reads as one space
// after skipping the next line's leading blanks.
func (x *xfile) xgetc() int {
	c := x.getc()
	if c == eof {
		return c
	}
	if c == '\\' {
		if c = x.getc(); c != '\n' {
			x.ungetcRaw(c)
			return '\\'
		}
		x.line++
		for {
			c = x.getc()
			if c != ' ' && c != '\t' {
				break
			}
		}
		x.ungetcRaw(c)
		return ' '
	}
	if c == '\n' {
		x.line++
	}
	return c
}

// xungetc (xio.c).
func (x *xfile) xungetc(c int) {
	if c == eof {
		return
	}
	x.ungetcRaw(c)
	if c == '\n' {
		x.line--
	}
}

// xskipcomment (xio.c): skip to the end of the line; returns '\n' or EOF.
func (x *xfile) xskipcomment() int {
	for {
		c := x.getc()
		if c == eof || c == '\n' {
			return c
		}
	}
}

// xskip (xio.c): skip characters in set; a '#' starts a comment.
func (x *xfile) xskip(set string) {
	var c int
	for {
		c = x.xgetc()
		if c == eof {
			break
		}
		if c == '#' {
			c = x.xskipcomment()
		}
		// strchr(str, c): c == EOF is (char)0xff, never in set; c == 0
		// matches the terminator
		if c == eof || (c != 0 && strings.IndexByte(set, byte(c)) < 0) {
			break
		}
	}
	x.xungetc(c)
}

func isspaceC(c int) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'
}

func isoctal(c byte) bool { return c >= '0' && c <= '7' }

// xgettok (xio.c) with sepa 0: a token ends at whitespace unless inside
// double quotes (quotes toggle and are dropped, and a quoted token runs
// across newlines); \ooo is decoded. Returns 1, 0 (no token: end of line
// or file) or -1 (longer than size-1).
func (x *xfile) xgettok(size int) (string, int) {
	tok := make([]byte, 0, 64)
	quoted := false
	c := 0
	for len(tok) < size {
		c = x.xgetc()
		if c == eof || !(quoted || !isspaceC(c)) {
			break
		}
		if c == '"' {
			quoted = !quoted
			continue
		}
		tok = append(tok, byte(c))
		i := len(tok)
		if i >= 4 && tok[i-4] == '\\' && isoctal(tok[i-3]) && isoctal(tok[i-2]) && isoctal(tok[i-1]) {
			if v, _ := strconv.ParseInt(string(tok[i-3:i]), 8, 32); v < 256 {
				tok = append(tok[:i-4], byte(v))
			}
		}
	}
	if c == '\n' {
		x.xungetc(c)
	}
	if len(tok) == 0 {
		return "", 0
	}
	if len(tok) >= size {
		return "", -1
	}
	return string(tok), 1
}

// ---- exports.c: getexportent ---------------------------------------------------

// Diag is a message nfs-utils would log.
type Diag struct {
	Line int
	Msg  string
}

// parser holds getexportent's statics (exports.c:54-59, 115).
type parser struct {
	x               *xfile
	fname           string
	first           bool
	hasDefaultOpts  bool
	defEE           Entry
	realpath        func(string) string
	diags           []Diag
	stoppedOnError  bool
	fromkernelFalse bool
}

func newParser(fname string, b []byte, realpath func(string) string) *parser {
	return &parser{x: &xfile{b: b, line: 1}, fname: fname, first: true, realpath: realpath}
}

func (p *parser) logf(format string, a ...any) {
	p.diags = append(p.diags, Diag{p.x.line, fmt.Sprintf(format, a...)})
}

const expTokSize = 512       // char exp[512] (exports.c:116)
const pathTokSize = 1024 + 1 // e_path[NFS_MAXPATHLEN+1] (nfslib.h:85)
const maxPath = 4096         // MAXPATHLEN for rpath
func (p *parser) getexport() (string, int) { // exports.c:947-957
	p.x.xskip(" \t")
	s, ok := p.x.xgettok(expTokSize)
	if ok < 0 {
		p.logf("syntax error")
	}
	return s, ok
}

func (p *parser) getpath() (string, int) { // exports.c:940-945
	p.x.xskip(" \t\n")
	return p.x.xgettok(pathTokSize)
}

// next is getexportent(0) (exports.c:112-211). nil is end of entries:
// end of file, or an error, after which export_read stops reading the
// file (export.c:122).
func (p *parser) next() *Entry {
	var exp string
	ok := 0
	if !p.first {
		exp, ok = p.getexport()
	}
	if p.first || ok == 0 {
		p.hasDefaultOpts = false
		p.defEE = initExportent()
		var path string
		path, ok = p.getpath()
		if ok <= 0 {
			return nil
		}
		p.defEE.Path = path
		exp, ok = p.getexport()
	}
	if ok < 0 {
		p.logf("expected client(options...)")
		p.stoppedOnError = true
		return nil
	}
	p.first = false

	if ok > 0 && exp[0] == '-' { // :150-161
		if !p.parseopts(exp[1:], &p.defEE, true) {
			p.stoppedOnError = true
			return nil
		}
		p.hasDefaultOpts = true
		exp, ok = p.getexport()
		if ok < 0 {
			p.logf("expected client(options...)")
			p.stoppedOnError = true
			return nil
		}
	}
	ee := p.defEE.clone() // :165
	if ok == 0 {          // :168-169
		exp = ""
	}
	hostname := exp
	var opt *string
	if i := strings.IndexByte(exp, '('); i >= 0 { // :172-182
		if i == 0 {
			p.logf("No host name given with %s %s, suggest *%s to avoid warning", ee.Path, exp, exp)
			hostname = "*"
		} else {
			hostname = exp[:i]
		}
		o := exp[i+1:]
		sp := strings.IndexByte(o, ')')
		if sp < 0 || sp+1 != len(o) {
			p.logf("syntax error: bad option list")
			p.stoppedOnError = true
			return nil
		}
		o = o[:sp]
		opt = &o
	} else if !p.hasDefaultOpts {
		p.logf("No options for %s %s: suggest %s(sync) to avoid warning", ee.Path, exp, exp)
	}
	ee.Hostname = hostname
	if opt != nil {
		if !p.parseopts(*opt, &ee, false) {
			p.stoppedOnError = true
			return nil
		}
	}
	// :203-208 nfsd_realpath; on failure the path is kept
	if p.realpath != nil {
		if r := p.realpath(ee.Path); r != "" {
			if len(r) > pathTokSize-1 {
				r = r[:pathTokSize-1]
			}
			ee.Path = r
		}
	}
	return &ee
}

// ---- exports.c: parseopts ------------------------------------------------------

// addFlavor is secinfo_addflavor (exports.c:468-485): the index of the
// flavor (by number), appended with the current e_flags if new.
func addFlavor(f *flavor, ep *Entry) int {
	for i, s := range ep.Secinfo {
		if s.Flavor == f.name || s.Num == f.num {
			return i
		}
	}
	if len(ep.Secinfo) >= secflavorCount {
		return -1
	}
	ep.Secinfo = append(ep.Secinfo, SecEntry{Flavor: f.name, Num: f.num, Flags: ep.Flags})
	return len(ep.Secinfo) - 1
}

func setflags(mask int, active uint, ep *Entry) { // exports.c:590-602
	ep.Flags |= mask
	for bit := 0; active != 0; bit, active = bit+1, active>>1 {
		if active&1 != 0 {
			ep.Secinfo[bit].Flags |= mask
		}
	}
}

func clearflags(mask int, active uint, ep *Entry) { // exports.c:605-617
	ep.Flags &^= mask
	for bit := 0; active != 0; bit, active = bit+1, active>>1 {
		if active&1 != 0 {
			ep.Secinfo[bit].Flags &^= mask
		}
	}
}

// cStrtol is strtol/strtoul's parse (leading space, sign, base 0 or 10):
// the value and the unparsed rest.
func cStrtol(s string, base int) (int64, string) {
	i := 0
	for i < len(s) && isspaceC(int(s[i])) {
		i++
	}
	neg := false
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		neg = s[i] == '-'
		i++
	}
	if base == 0 {
		switch {
		case i+1 < len(s) && s[i] == '0' && (s[i+1] == 'x' || s[i+1] == 'X') && i+2 < len(s) && isHex(s[i+2]):
			base, i = 16, i+2
		case i < len(s) && s[i] == '0':
			base = 8
		default:
			base = 10
		}
	}
	start := i
	var v int64
	for i < len(s) {
		d := digitVal(s[i])
		if d < 0 || d >= base {
			break
		}
		if v < 1<<60 {
			v = v*int64(base) + int64(d)
		}
		i++
	}
	if i == start {
		return 0, s // no digits: endptr = nptr
	}
	if neg {
		v = -v
	}
	return v, s[i:]
}

func isHex(c byte) bool { return digitVal(c) >= 0 && digitVal(c) < 16 }

func digitVal(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return -1
}

func isblankC(c byte) bool { return c == ' ' || c == '\t' }

// parseopts (exports.c:637-864). It returns false on a bad option, which
// makes getexportent fail.
func (p *parser) parseopts(cp string, ep *Entry, _ bool) bool {
	var active uint
	sawReexport := false
	i := 0
	for i < len(cp) && isblankC(cp[i]) {
		i++
	}
	for i < len(cp) {
		j := strings.IndexByte(cp[i:], ',')
		var opt string
		if j < 0 {
			opt, i = cp[i:], len(cp)
		} else {
			opt, i = cp[i:i+j], i+j+1
		}
		switch {
		case opt == "ro":
			setflags(ExpReadonly, active, ep)
		case opt == "rw":
			clearflags(ExpReadonly, active, ep)
		case opt == "secure":
			clearflags(ExpInsecurePort, active, ep)
		case opt == "insecure":
			setflags(ExpInsecurePort, active, ep)
		case opt == "sync":
			clearflags(ExpAsync, active, ep)
		case opt == "async":
			setflags(ExpAsync, active, ep)
		case opt == "nordirplus":
			setflags(ExpNoReaddirPlus, active, ep)
		case opt == "security_label":
			setflags(ExpSecurityLabel, active, ep)
		case opt == "sign_fh":
			setflags(ExpSignFH, active, ep)
		case opt == "nohide":
			setflags(ExpNoHide, active, ep)
		case opt == "hide":
			clearflags(ExpNoHide, active, ep)
		case opt == "crossmnt":
			setflags(ExpCrossMount, active, ep)
		case opt == "nocrossmnt":
			clearflags(ExpCrossMount, active, ep)
		case opt == "wdelay":
			setflags(ExpGatheredWrites, active, ep)
		case opt == "no_wdelay":
			clearflags(ExpGatheredWrites, active, ep)
		case opt == "root_squash":
			setflags(ExpRootSquash, active, ep)
		case opt == "no_root_squash":
			clearflags(ExpRootSquash, active, ep)
		case opt == "all_squash":
			setflags(ExpAllSquash, active, ep)
		case opt == "no_all_squash":
			clearflags(ExpAllSquash, active, ep)
		case opt == "subtree_check":
			clearflags(ExpNoSubtreeCheck, active, ep)
		case opt == "no_subtree_check":
			setflags(ExpNoSubtreeCheck, active, ep)
		case opt == "auth_nlm", opt == "secure_locks":
			clearflags(ExpNoAuthNLM, active, ep)
		case opt == "no_auth_nlm", opt == "insecure_locks":
			setflags(ExpNoAuthNLM, active, ep)
		case opt == "acl":
			clearflags(ExpNoACL, active, ep)
		case opt == "no_acl":
			setflags(ExpNoACL, active, ep)
		case opt == "pnfs":
			setflags(ExpPNFS, active, ep)
		case opt == "no_pnfs":
			clearflags(ExpPNFS, active, ep)
		case strings.HasPrefix(opt, "anonuid="), strings.HasPrefix(opt, "anongid="): // :725-742
			v, rest := cStrtol(opt[8:], 10)
			if opt[8:] == "" || rest != "" {
				p.logf("%s: bad %s %q", p.fname, opt[:7], opt)
				return false
			}
			if opt[4] == 'u' {
				ep.AnonUID = int64(uint32(v))
			} else {
				ep.AnonGID = int64(uint32(v))
			}
		case strings.HasPrefix(opt, "squash_uids="), strings.HasPrefix(opt, "squash_gids="): // :743-750
			// parsesquash sees opt, already cut at the next ',', so a
			// list stops at its first id range (exports.c:655-662, 874-904)
			ids, ok := parsesquash(opt[12:])
			if !ok {
				p.logf("syntax error: bad uid/gid list")
				return false
			}
			if opt[7] == 'u' {
				ep.Squids = append(ep.Squids, ids...)
			} else {
				ep.Sqgids = append(ep.Sqgids, ids...)
			}
		case strings.HasPrefix(opt, "fsid="): // :751-774
			if sawReexport {
				p.logf("'fsid=' has to be before 'reexport=' %s", opt)
				return false
			}
			if opt[5:] == "root" {
				ep.FsidNum = 0
				setflags(ExpFSID, active, ep)
			} else {
				v, rest := cStrtol(opt[5:], 0)
				switch {
				case opt[5:] != "" && rest == "":
					ep.FsidNum = uint64(v)
					setflags(ExpFSID, active, ep)
				case validUUID(opt[5:]):
					ep.UUID = opt[5:]
				default:
					p.logf("bad fsid %q", opt)
					return false
				}
			}
		case opt == "mountpoint", opt == "mp", strings.HasPrefix(opt, "mountpoint="), strings.HasPrefix(opt, "mp="): // :775-783
			m := ""
			if k := strings.IndexByte(opt, '='); k >= 0 {
				m = opt[k+1:]
			}
			ep.Mountpt = &m
		case strings.HasPrefix(opt, "refer="): // :794-796
			ep.FslocKind, ep.FslocData = "refer", opt[6:]
		case strings.HasPrefix(opt, "replicas="): // :797-799
			ep.FslocKind, ep.FslocData = "replicas", opt[9:]
		case strings.HasPrefix(opt, "sec="): // :800-803, parse_flavors :500-518
			active = 0
			for _, name := range strings.Split(opt[4:], ":") {
				f := findFlavor(name)
				if f == nil {
					p.logf("unknown flavor %s", name)
					return false
				}
				bit := addFlavor(f, ep)
				if bit < 0 {
					return false
				}
				active |= 1 << bit
			}
		case strings.HasPrefix(opt, "xprtsec="): // :804-806
			for _, name := range strings.Split(opt[8:], ":") {
				if name != "none" && name != "tls" && name != "mtls" {
					p.logf("unknown xprtsec mode %s", name)
					return false
				}
				dup := false
				for _, x := range ep.Xprtsec {
					dup = dup || x == name
				}
				if !dup {
					ep.Xprtsec = append(ep.Xprtsec, name)
				}
			}
		case strings.HasPrefix(opt, "reexport="): // :807-841
			s := opt[9:]
			if sawReexport || (s != "auto-fsidnum" && s != "predefined-fsidnum" && s != "none") {
				p.logf("bad option %s", opt)
				return false
			}
			// reexpdb_apply_reexport_settings needs the reexport
			// database (ambiguity: not modelled; fsid left as is)
			ep.Reexport = s
			sawReexport = true
		default: // :842-847
			p.logf("unknown keyword %q", opt)
			setflags(ExpAllSquash|ExpReadonly, active, ep)
			return false
		}
		for i < len(cp) && isblankC(cp[i]) {
			i++
		}
	}
	fixPseudoflavorFlags(ep)
	return true
}

// fixPseudoflavorFlags (exports.c:624-632).
func fixPseudoflavorFlags(ep *Entry) {
	for k := range ep.Secinfo {
		ep.Secinfo[k].Flags |= ep.Flags &^ SecinfoFlags
	}
}

// parsesquash (exports.c:866-909) on one option's value: ids are N or
// N-M separated by ','; it stops at the end or at ')'.
func parsesquash(s string) ([]int, bool) {
	var ids []int
	i := 0
	num := func() int { // parsenum :926-938: atoi of [-]digits
		start := i
		if i < len(s) && s[i] == '-' {
			i++
		}
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			i++
		}
		v, _ := strconv.Atoi(s[start:i])
		return v
	}
	for {
		id0 := num()
		id1 := id0
		if i < len(s) && s[i] == '-' {
			i++
			id1 = num()
		}
		if id0 == -1 || id1 == -1 {
			return nil, false
		}
		ids = append(ids, id0, id1)
		if i >= len(s) || s[i] == ')' || (s[i] == ',' && !(i+1 < len(s) && s[i+1] >= '0' && s[i+1] <= '9')) {
			break
		}
		if s[i] != ',' {
			return nil, false
		}
		i++
	}
	return ids, true
}

func validUUID(s string) bool { // exports.c:453-461
	n := 0
	for i := 0; i < len(s); i++ {
		if isHex(s[i]) {
			n++
		}
	}
	return n == 32
}

// ---- exports.c: putexportent -----------------------------------------------------

// fprintpath (exports.c:266-275): control characters, '"', '\\', '#' and
// whitespace as \ooo.
func fprintpath(b *strings.Builder, path string) {
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c < 0x20 || c == 0x7f || c == '"' || c == '\\' || c == '#' || isspaceC(int(c)) {
			fmt.Fprintf(b, "\\%03o", c)
		} else {
			b.WriteByte(c)
		}
	}
}

// ensureSecinfo is secinfo_show's side effect (exports.c:243-244): an
// entry without a flavor gets "sys" with the entry's flags.
func ensureSecinfo(ep *Entry) {
	if len(ep.Secinfo) == 0 {
		addFlavor(findFlavor("sys"), ep)
	}
}

// PutExportent renders one etab line (putexportent, exports.c:277-383,
// secinfo_show :236-253, xprtsecinfo_show :255-264).
func PutExportent(ep Entry) string {
	ensureSecinfo(&ep)
	var b strings.Builder
	fprintpath(&b, ep.Path)
	f := ep.Flags
	yn := func(cond bool, y, n string) string {
		if cond {
			return y
		}
		return n
	}
	fmt.Fprintf(&b, "\t%s(", ep.Hostname)
	b.WriteString(yn(f&ExpReadonly != 0, "ro,", "rw,"))
	b.WriteString(yn(f&ExpAsync != 0, "async,", "sync,"))
	b.WriteString(yn(f&ExpGatheredWrites != 0, "wdelay,", "no_wdelay,"))
	b.WriteString(yn(f&ExpNoHide != 0, "nohide,", "hide,"))
	b.WriteString(yn(f&ExpCrossMount != 0, "crossmnt,", "nocrossmnt,"))
	b.WriteString(yn(f&ExpInsecurePort != 0, "insecure,", "secure,"))
	b.WriteString(yn(f&ExpRootSquash != 0, "root_squash,", "no_root_squash,"))
	b.WriteString(yn(f&ExpAllSquash != 0, "all_squash,", "no_all_squash,"))
	b.WriteString(yn(f&ExpNoSubtreeCheck != 0, "no_subtree_check,", "subtree_check,"))
	b.WriteString(yn(f&ExpNoAuthNLM != 0, "insecure_locks,", "secure_locks,"))
	b.WriteString(yn(f&ExpNoACL != 0, "no_acl,", "acl,"))
	if f&ExpNoReaddirPlus != 0 {
		b.WriteString("nordirplus,")
	}
	if f&ExpSecurityLabel != 0 {
		b.WriteString("security_label,")
	}
	if f&ExpSignFH != 0 {
		b.WriteString("sign_fh,")
	}
	b.WriteString(yn(f&ExpPNFS != 0, "pnfs,", "no_pnfs,"))
	if f&ExpFSID != 0 {
		fmt.Fprintf(&b, "fsid=%d,", int32(ep.FsidNum))
	}
	if ep.UUID != "" {
		fmt.Fprintf(&b, "fsid=%s,", ep.UUID)
	}
	if ep.Reexport != "" && ep.Reexport != "none" {
		fmt.Fprintf(&b, "reexport=%s,", ep.Reexport)
	}
	if ep.Mountpt != nil {
		b.WriteString("mountpoint")
		if *ep.Mountpt != "" {
			b.WriteString("=" + *ep.Mountpt)
		}
		b.WriteString(",")
	}
	if ep.FslocKind != "" {
		b.WriteString(ep.FslocKind + "=")
		fprintpath(&b, ep.FslocData)
		b.WriteString(",")
	}
	if ep.Squids != nil {
		b.WriteString("squash_uids=")
		writeIDs(&b, ep.Squids, len(ep.Squids))
	}
	if ep.Sqgids != nil {
		b.WriteString("squash_gids=")
		writeIDs(&b, ep.Sqgids, len(ep.Squids)) // sic: e_nsquids (exports.c:373)
	}
	fmt.Fprintf(&b, "anonuid=%d,anongid=%d", int32(ep.AnonUID), int32(ep.AnonGID))
	for k := 0; k < len(ep.Secinfo); {
		s := ep.Secinfo[k]
		b.WriteString(",sec=" + s.Flavor)
		k++
		for k < len(ep.Secinfo) && ep.Secinfo[k].Flags == s.Flags {
			b.WriteString(":" + ep.Secinfo[k].Flavor)
			k++
		}
		for _, m := range []struct {
			flag     int
			set, uns string
		}{{ExpReadonly, "ro", "rw"}, {ExpInsecurePort, "insecure", "secure"}, {ExpRootSquash, "root_squash", "no_root_squash"}, {ExpAllSquash, "all_squash", "no_all_squash"}} {
			if SecinfoFlags&m.flag != 0 {
				b.WriteString("," + yn(s.Flags&m.flag != 0, m.set, m.uns))
			}
		}
	}
	if len(ep.Xprtsec) > 0 {
		b.WriteString(",xprtsec=" + strings.Join(ep.Xprtsec, ":"))
	}
	b.WriteString(")\n")
	return b.String()
}

func writeIDs(b *strings.Builder, id []int, n int) {
	for k := 0; k+1 < n && k+1 < len(id); k += 2 {
		if id[k] != id[k+1] {
			fmt.Fprintf(b, "%d-%d,", id[k], id[k+1])
		} else {
			fmt.Fprintf(b, "%d,", id[k])
		}
	}
}
