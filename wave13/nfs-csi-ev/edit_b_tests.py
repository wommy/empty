import sys, re
root = sys.argv[1]

def rd(path):
    return open(root + '/' + path).read()

def wr(path, s):
    open(root + '/' + path, 'w').write(s)

def sub(s, old, new, count=1):
    if old not in s:
        raise SystemExit('missing: ' + old[:120])
    return s.replace(old, new, count)

f = 'internal/endpoint/export_test.go'
s = rd(f)
s = sub(s, '''const clientsProp = "democratic-csi:nfs_clients"

''', '')
s = sub(s, '''// exported is a volume the endpoint already exports with clients enc.
func exported(id, enc, share string) Row {
	return fsv(id, func(r *Row) {
		r.NFSClients, r.NFSClientsSet = enc, true
		r.ShareNFS = share
	})
}''', '''// exported is a volume the endpoint already exports: share is its local
// sharenfs.
func exported(id, share string) Row {
	return fsv(id, func(r *Row) { r.ShareNFS = share })
}''')
# drop the stored-encoding half of TestShareNFSAndClientEncoding
i = s.index('	enc := encodeClients(cs)\n	if enc != ')
j = s.index('// ADR 0004, csi-nfs-design §2: the canonical grammar.')
s = s[:i] + '}\n\n' + s[j:]
s = sub(s, 'func TestShareNFSAndClientEncoding(t *testing.T) {', 'func TestShareNFS(t *testing.T) {')

# exported("a", "<enc>", "<share>") -> exported("a", "<share>")
s = re.sub(r'exported\("a", ("[^"]*"|encodeClients\(many\)), ', 'exported("a", ', s)
# the stored list never appears in a plan
s = re.sub(r'clientsProp: "[^"]*", ', '', s)
s = re.sub(r', clientsProp: "[^"]*"', '', s)
s = re.sub(r'clientsProp: "[^"]*"', '', s)
open('/dev/null', 'w')
wr(f, s)
