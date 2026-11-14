package main

import (
	"net"
	"os"
)

// sdNotify sends state (e.g. "READY=1") to systemd's $NOTIFY_SOCKET, the
// sd_notify(3) protocol: one datagram on a unix socket, a leading '@'
// naming an abstract one (Go's net maps it). Without the variable (not
// under systemd, or Type= is not notify) it does nothing. Errors are
// returned for logging only: a lost notification must not stop the daemon.
func sdNotify(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: addr, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte(state))
	return err
}
