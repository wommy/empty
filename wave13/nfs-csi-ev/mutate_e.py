import os, shutil, subprocess, sys
src = sys.argv[1]
out = sys.argv[2]
f = 'internal/csi/controller.go'
mutants = {
    'E1 no unadvertised Message': ('m.nfs && s != "" && !slices.Contains(m.servers, s)', 'false && s != ""'),
    'E2 export always rw': ('ReadOnly: readOnly(r.GetVolumeCapabilities())', 'ReadOnly: false'),
    'E3 writer on ro export confirmed': ('!readOnly([]*csi.VolumeCapability{vc}) && n.GetReadOnly()', 'false'),
    'E4 clients not required': ('if _, ok := in[ParamNFSClients]; m.nfs && !ok {', 'if _, ok := in[ParamNFSClients]; false && m.nfs && !ok {'),
    'E5 server/share for every filesystem': ('if s := v.GetNfs().GetServer(); s != "" && v.GetMountpoint() != "" {', 'if s := v.GetNfs().GetServer(); v.GetMountpoint() != "" {'),
}
log = open(out, 'w')
for name, (old, new) in mutants.items():
    work = src + '-mut'
    shutil.rmtree(work, ignore_errors=True)
    shutil.copytree(src, work)
    p = os.path.join(work, f)
    s = open(p).read()
    if old not in s:
        log.write('### %s: anchor missing\n' % name)
        continue
    open(p, 'w').write(s.replace(old, new, 1))
    r = subprocess.run(['go', 'test', '-count=1', './internal/csi', '-run', 'TestNFS'], cwd=work, capture_output=True, text=True)
    lines = [l for l in (r.stdout + r.stderr).splitlines() if l.startswith(('--- FAIL', '    ', 'FAIL', 'ok'))]
    log.write('### %s (exit %d)\n%s\n\n' % (name, r.returncode, '\n'.join(lines[:20])))
    log.flush()
shutil.rmtree(src + '-mut', ignore_errors=True)
