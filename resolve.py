import re,sys
def res(path, pick):
    s=open(path).read()
    pat=re.compile(r'<<<<<<< HEAD\n(.*?)=======\n(.*?)>>>>>>> fixes-lifecycle\n',re.S)
    i=[0]
    def f(m):
        r=pick(i[0],m.group(1),m.group(2)); i[0]+=1; return r
    open(path,'w').write(pat.sub(f,s))
def main_pick(n,o,t):
    if n==0:
        return t.replace('\tdestroyForeign := fs.Bool','\tnfsMinPrefix := fs.Int("nfs-min-prefix", endpoint.DefaultNFSMinPrefix, "broadest IPv4 client network PublishFileExport accepts (prefix length, 1-32)")\n\tdestroyForeign := fs.Bool')
    return t.replace('DestroyForeignSnapshots: *destroyForeign}','DestroyForeignSnapshots: *destroyForeign, NFSMinPrefix: *nfsMinPrefix}')
res('cmd/zfs-endpoint/main.go',main_pick)
res('internal/server/server.go',lambda n,o,t:t.replace('&pb.ReconcileResponse{}','&pb.ReconcileResponse{PlanDigest: plan.Digest}'))
TRY='''// TryAcquire locks every key only if none is held, without waiting. It
// reports false (and holds nothing) when any key is busy. Never waiting
// means it cannot take part in a wait cycle, whatever the key order.
func (k *Keyed) TryAcquire(keys ...string) (func(), bool) {
	keys = dedupe(keys)
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, name := range keys {
		if _, busy := k.held[name]; busy {
			return nil, false
		}
	}
	if k.held == nil {
		k.held = map[string]*key{}
	}
	for _, name := range keys {
		k.held[name] = &key{}
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			k.mu.Lock()
			defer k.mu.Unlock()
			for _, name := range keys {
				k.unlock(name)
			}
		})
	}, true
}

'''
res('internal/lock/lock.go',lambda n,o,t:TRY+t)
res('internal/lock/lock_test.go',lambda n,o,t:o+t)
