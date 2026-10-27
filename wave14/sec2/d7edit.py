import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a031c2bb39fd727ca')
def sub(p, old, new):
    s=open(p).read()
    assert s.count(old)==1, (p, old, s.count(old))
    open(p,'w').write(s.replace(old,new))
M='cmd/zfs-endpoint/main.go'
sub(M,'''	showVersion := fs.Bool("version", false, "print the version and exit")''','''	socketGroup := fs.String("socket-group", "", "group name or gid given the -listen socket: the socket becomes 0660 and its directory 0710, so a non-root CSI controller in the group (supplementalGroups) can connect. "+
		"The directory must hold nothing but the socket. Default none: the socket stays 0600, owner only. The admin socket is never shared")
	showVersion := fs.Bool("version", false, "print the version and exit")''')
sub(M,'''	adminSock, err := adminSocketPath(*adminListen, sock, d.lockDir)
	if err != nil {
		return err
	}''','''	adminSock, err := adminSocketPath(*adminListen, sock, d.lockDir)
	if err != nil {
		return err
	}
	gid, err := socketGID(*socketGroup)
	if err != nil {
		return err
	}''')
sub(M,'''	lis, err := unixsock.Listen(sock)
	if err != nil {
		return err
	}''','''	lis, err := listenCSI(sock, gid)
	if err != nil {
		return err
	}''')
sub(M,'''// socketPath accepts only unix:///absolute/path.''','''// noGroup is socketGID's "no -socket-group".
const noGroup = -1

// socketGID resolves -socket-group, a group name or a numeric gid, to a
// gid; "" is noGroup. An unknown group is a configuration error (78).
func socketGID(group string) (int, error) {
	if group == "" {
		return noGroup, nil
	}
	if n, err := strconv.Atoi(group); err == nil {
		if n < 0 {
			return 0, configErr("-socket-group %d: want a gid of 0 or more", n)
		}
		return n, nil
	}
	g, err := user.LookupGroup(group)
	if err != nil {
		return 0, configErr("-socket-group %q: %v", group, err)
	}
	n, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, configErr("-socket-group %q: gid %q is not a number", group, g.Gid)
	}
	return n, nil
}

// listenCSI listens on the -listen socket, owner only (unixsock.Listen),
// and, given a gid, opens it to that group (unixsock.ShareWithGroup, ADR
// 0001 v2 D7). A directory that holds more than the socket is a
// configuration error: the operator chose the path.
func listenCSI(sock string, gid int) (net.Listener, error) {
	lis, err := unixsock.Listen(sock)
	if err != nil || gid == noGroup {
		return lis, err
	}
	if err := unixsock.ShareWithGroup(sock, gid); err != nil {
		lis.Close()
		return nil, configErr("-socket-group: %v", err)
	}
	return lis, nil
}

// socketPath accepts only unix:///absolute/path.''')
sub(M,'''	"os/signal"
''','''	"os/signal"
	"os/user"
''')
sub(M,'''	"path/filepath"
	"strings"
''','''	"path/filepath"
	"strconv"
	"strings"
''')
