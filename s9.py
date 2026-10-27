import os
os.chdir('/home/user/empty/.claude/worktrees/agent-a5205796387710ae5')

p='internal/unixsock/unixsock.go'
s=open(p).read()
old='''// CheckPath accepts only an absolute filesystem path for a socket. Go
// treats a leading '@' as a Linux abstract socket: no inode, so no mode,
// no owner and no directory to protect it, and any local process can
// connect.
func CheckPath(path string) error {
	if strings.HasPrefix(path, "@") {
		return fmt.Errorf("socket %q: abstract sockets have no permissions; use an absolute path", path)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("socket %q: want a clean absolute path", path)
	}
	return nil
}'''
new='''// MaxPathLen is the longest socket path bind accepts: sockaddr_un's
// sun_path holds 108 bytes including the terminating NUL.
const MaxPathLen = 107

// CheckPath accepts only an absolute filesystem path for a socket, short
// enough to bind. Go treats a leading '@' as a Linux abstract socket: no
// inode, so no mode, no owner and no directory to protect it, and any
// local process can connect. A path past MaxPathLen would pass every other
// check and fail only at bind ("invalid argument"), after the daemon's
// whole startup.
func CheckPath(path string) error {
	if strings.HasPrefix(path, "@") {
		return fmt.Errorf("socket %q: abstract sockets have no permissions; use an absolute path", path)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("socket %q: want a clean absolute path", path)
	}
	if len(path) > MaxPathLen {
		return fmt.Errorf("socket %q is %d bytes: sun_path holds %d and a NUL (108 bytes)", path, len(path), MaxPathLen)
	}
	return nil
}'''
assert old in s
s=s.replace(old,new)
open(p,'w').write(s)

p='internal/unixsock/unixsock_test.go'
s=open(p).read()
old='''// An existing socket directory others could write'''
new='''// A path bind cannot take (sun_path: 107 bytes and a NUL) is refused up
// front, by Listen and by Target alike; 107 bytes is served.
func TestSocketPathFitsSunPath(t *testing.T) {
	dir := t.TempDir()
	at := filepath.Join(dir, strings.Repeat("x", unixsock.MaxPathLen-len(dir)-1))
	if len(at) != unixsock.MaxPathLen {
		t.Fatalf("setup: %d bytes", len(at))
	}
	l, err := unixsock.Listen(at)
	if err != nil {
		t.Fatalf("%d bytes: %v", len(at), err)
	}
	l.Close()
	over := at + "x"
	if l, err := unixsock.Listen(over); err == nil || !strings.Contains(err.Error(), "108") {
		if l != nil {
			l.Close()
		}
		t.Errorf("%d bytes: %v", len(over), err)
	}
	if _, err := unixsock.Target("unix://" + over); err == nil {
		t.Errorf("Target accepted %d bytes", len(over))
	}
}

// An existing socket directory others could write'''
assert old in s
s=s.replace(old,new,1)
open(p,'w').write(s)

p='cmd/zfs-endpoint/main.go'
s=open(p).read()
old='''	sock, err := socketPath(listenAddr(*listen, *parent))
	if err != nil {
		return err
	}'''
new='''	sock, err := socketPath(listenAddr(*listen, *parent))
	if err != nil {
		if *listen == "" {
			// the unit's %i.sock: each byte of -parent outside
			// [A-Za-z0-9:_.], '-' included, is \\xNN there, 4 bytes
			return configErr("%v (the default socket, named after the systemd-escaped -parent); pass a shorter -listen", err)
		}
		return err
	}'''
assert old in s
s=s.replace(old,new)
open(p,'w').write(s)

p='cmd/zfs-endpoint/lockparent.go'
s=open(p).read()
old='''		path := filepath.Join(dir, strings.ReplaceAll(name, "/", "%")+".lock")
'''
new='''		base := strings.ReplaceAll(name, "/", "%") + ".lock"
		if len(base) > maxNameLen {
			unlock()
			return nil, configErr("parent %s: lock file name %s is %d bytes, over the %d a file name may have", parent, base, len(base), maxNameLen)
		}
		path := filepath.Join(dir, base)
'''
assert old in s
s=s.replace(old,new)
old='''			if errors.Is(err, syscall.EWOULDBLOCK) {
				if name != parent {
					return nil, fmt.Errorf("another endpoint already manages %s, which contains %s (lock %s)", name, parent, path)
				}
				return nil, fmt.Errorf("another endpoint already manages %s or a dataset inside it (lock %s)", parent, path)
			}'''
new='''			// a conflict is configuration: the other endpoint is still
			// there when systemd restarts this one, so exit 78, which
			// stops the restart loop (systemd itself waits for a stopping
			// instance before it starts the next)
			if errors.Is(err, syscall.EWOULDBLOCK) {
				if name != parent {
					return nil, configErr("another endpoint already manages %s, which contains %s (lock %s)", name, parent, path)
				}
				return nil, configErr("another endpoint already manages %s or a dataset inside it (lock %s)", parent, path)
			}'''
assert old in s
s=s.replace(old,new)
old='''const lockDir = "/run/zfs-endpoint"
'''
new='''const lockDir = "/run/zfs-endpoint"

// maxNameLen is NAME_MAX: a dataset name may have 255 bytes, its lock
// file name 5 more.
const maxNameLen = 255
'''
assert old in s
s=s.replace(old,new)
open(p,'w').write(s)
