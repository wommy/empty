p='deploy/systemd/zfs-endpoint-reconcile@.service'
s=open(p).read()
old='''# It talks to the admin socket, /run/zfs-endpoint/admin/%i/endpoint.sock,
# served to root (or the daemon's uid) only, so it runs as root.
'''
new='''# It talks to the admin socket, /run/zfs-endpoint/admin/%i/endpoint.sock,
# served to root (or the daemon's uid) only, so it runs as root. A daemon
# whose -admin-listen a drop-in shortened (a long dataset name,
# zfs-endpoint@.service) needs the same path here, in a drop-in of this
# instance, e.g. zfs-endpoint-reconcile@tank-k8s-vols.service.d/admin.conf:
#
#   [Service]
#   ExecStart=
#   ExecStart=/usr/local/sbin/zfs-endpoint reconcile -admin-socket unix:///run/zfs-endpoint/admin/vols/endpoint.sock -timeout 5m
#
# Unlike the daemon it mounts nothing, so it takes a mount namespace and
# no capability: root owns the socket and its directories, so connect(2)
# needs none (a daemon run as another uid: add CAP_DAC_OVERRIDE in a
# drop-in), and a socket is reached on a read-only mount. Each setting is
# checked by starting this unit under ci/real-zfs-unit.sh.
'''
assert old in s
s=s.replace(old,new)
old='''# a client of one unix socket
RestrictAddressFamilies=AF_UNIX
'''
new='''# a client of one unix socket
RestrictAddressFamilies=AF_UNIX
ProtectSystem=strict
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictNamespaces=yes
CapabilityBoundingSet=
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM
'''
assert old in s
s=s.replace(old,new)
open(p,'w').write(s)

p='deploy/deploy_test.go'
s=open(p).read()
old='''		if fi, err := os.Stat(f); err != nil || fi.IsDir() {
			continue
		}
		active, commented := directives(t, f)
		for _, k := range noNamespace {'''
new='''		if fi, err := os.Stat(f); err != nil || fi.IsDir() {
			continue
		}
		if strings.HasPrefix(filepath.Base(f), "zfs-endpoint-reconcile@") {
			continue // a socket client that mounts nothing (TestReconcileTimerIsReportOnly)
		}
		active, commented := directives(t, f)
		for _, k := range noNamespace {'''
assert old in s
s=s.replace(old,new)
open(p,'w').write(s)
