//go:build linux || darwin

package process

import (
	"net"
	"os"
	"path"
	"strconv"
	"syscall"
	"time"
)

const (
	// guardEvery is how often a unit's port is looked at until it listens.
	guardEvery = 250 * time.Millisecond
	// guardDial is how long one address has to take a connection.
	guardDial = 500 * time.Millisecond
	// openNote is the last line of the log of a unit killed by guard.
	openNote = "javo: stopped this app: it listens on %s, which other machines on the network can reach. An app that runs as a process must listen on 127.0.0.1 only."
)

// guard kills the unit when its port takes connections on an address of
// this machine other than loopback: nothing stands between such an app
// and the network the machine is on. It looks once, when the port first
// answers on loopback; a unit that opens the port to the network later,
// or listens only on an IPv6 link-local address, is not caught. Until it
// has looked, an app that listens on every address can be reached.
func (p *proc) guard(port int) {
	tick := time.NewTicker(guardEvery)
	defer tick.Stop()
	for !dials(loopbackIP, port) {
		select {
		case <-p.exited:
			return
		case <-tick.C:
		}
	}
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.IsLoopback() || n.IP.IsLinkLocalUnicast() && n.IP.To4() == nil {
			continue
		}
		if dials(n.IP.String(), port) {
			p.mu.Lock()
			if !p.gone {
				p.open = net.JoinHostPort(n.IP.String(), strconv.Itoa(port))
				_ = syscall.Kill(-p.pid, syscall.SIGKILL)
			}
			p.mu.Unlock()
			return
		}
	}
}

const loopbackIP = "127.0.0.1"

// dials reports whether host takes a connection on port.
func dials(host string, port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), guardDial)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// note adds a line of Javo's own to the closed log of the unit of the
// workload in wd.
func (e *engine) note(wd, unit, line string) {
	f, err := e.root.OpenFile(path.Join(wd, "logs", unit+".log"), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString(time.Now().UTC().Format(stampLayout) + " " + line + "\n")
}
