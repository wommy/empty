import re
p='internal/csi/controller_test.go'
s=open(p).read()
lines=s.split('\n')
# line numbers (1-based) of controllers that create or validate mount volumes outside NFS mode
targets={73,121,179,180,252,271,318,323,339,365}
for i in targets:
    l=lines[i-1]
    assert 'zcsi.Controller{' in l, (i,l)
    lines[i-1]=l.replace('}', ', EnableLocalFilesystem: true}',1) if l.rstrip().endswith('}') else l
    assert 'EnableLocalFilesystem' in lines[i-1], (i,l)
s='\n'.join(lines)
open(p,'w').write(s)

p='internal/csi/nfs_test.go'
s=open(p).read()
a='''	f.ctl = &zcsi.Controller{EP: pb.NewEndpointServiceClient(conn)}'''
assert s.count(a)==1
s=s.replace(a,'''	// without servers the endpoint is not in NFS mode: its mount volumes
	// are the ones -enable-local-filesystem makes (ADR 0009), what a
	// pre-NFS deployment holds
	f.ctl = &zcsi.Controller{EP: pb.NewEndpointServiceClient(conn), EnableLocalFilesystem: true}''')
open(p,'w').write(s)

p='internal/csi/sanity_test.go'
s=open(p).read()
a='''	csi.RegisterControllerServer(srv, &zcsi.Controller{EP: ep, EnableBlock: at == "block"})'''
assert s.count(a)==1
s=s.replace(a,'''	// mount mode without NFS provisions node-local filesystems, which
	// are opt-in (ADR 0009)
	csi.RegisterControllerServer(srv, &zcsi.Controller{EP: ep, EnableBlock: at == "block", EnableLocalFilesystem: !nfs && at != "block"})''')
open(p,'w').write(s)
