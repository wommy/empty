import os, shutil, subprocess, sys
src = sys.argv[1]   # pristine D tree
out = sys.argv[2]   # evidence file
mutants = {
    'M1 commit without verifyExport': ('internal/endpoint/endpoint.go',
        '''			if err := e.verifyExport(ctx, id); err != nil {
				return Volume{}, err
			}''', ''),
    'M2 server ignored by idempotency': ('internal/endpoint/plan.go',
        '''	if w.explicit && row.NFSServer != w.server {''', '''	if false && w.explicit && row.NFSServer != w.server {'''),
    'M3 server recomputed from the advertised list': ('internal/endpoint/endpoint.go',
        '''		v.NFS = &NFSExport{Server: r.NFSServer, ReadOnly: len(cs) > 0}''',
        '''		v.NFS = &NFSExport{Server: r.NFSServer, ReadOnly: len(cs) > 0}
		if r.NFSServer != "" && len(e_servers) > 0 {
			v.NFS.Server = e_servers[0]
		}'''),
    'M4 no unadvertised report': ('internal/endpoint/reconcile.go',
        '''			skip(r.ID, SkipNFSServerUnadvertised)''', ''),
    'M5 inherited server read as the volume\'s': ('internal/endpoint/table.go',
        '''	r.NFSServer = own(cNFSServer)''', '''	r.NFSServer = userValue(cell(cNFSServer))'''),
}
log = open(out, 'w')
for name, (f, old, new) in mutants.items():
    work = src + '-mut'
    shutil.rmtree(work, ignore_errors=True)
    shutil.copytree(src, work)
    p = os.path.join(work, f)
    s = open(p).read()
    if old not in s:
        log.write('### %s: anchor missing\n' % name)
        continue
    s = s.replace(old, new, 1)
    if 'e_servers' in new:
        s = s.replace('var _ = 0', '')
        s += '\nvar e_servers = []string{"192.0.2.10"}\n'
    open(p, 'w').write(s)
    r = subprocess.run(['go', 'test', '-count=1', './internal/endpoint',
                        '-run', 'NFS|TestFileExportCreate'], cwd=work,
                       capture_output=True, text=True)
    lines = [l for l in (r.stdout + r.stderr).splitlines() if l.startswith(('--- FAIL', '    ', 'FAIL', 'ok'))]
    log.write('### %s (exit %d)\n%s\n\n' % (name, r.returncode, '\n'.join(lines[:30])))
    log.flush()
shutil.rmtree(src + '-mut', ignore_errors=True)
