package netprobe

import (
	"context"
	"errors"
	"net"
	"sync"
	"syscall"
	"time"
)

// How an address answered a TCP connection. Connected and Refused both
// mean packets got there and back (a refusal is the far side's answer),
// so either is "reached". NoRoute is what a network without a way out
// gives at once; TimedOut is silence.
const (
	Connected = "connected"
	Refused   = "refused"
	NoRoute   = "no route"
	TimedOut  = "timed out"
)

// Reached reports whether how (a value of Connect) means the address
// answered.
func Reached(how string) bool { return how == Connected || how == Refused }

// Connect opens a TCP connection to each address ("host:port"), all at
// once and each within timeout, and tells how each answered: one of the
// four words above, or "failed: " and the error.
func Connect(ctx context.Context, addrs []string, timeout time.Duration) map[string]string {
	out := make(map[string]string, len(addrs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, addr := range addrs {
		wg.Go(func() {
			how := connect(ctx, addr, timeout)
			mu.Lock()
			out[addr] = how
			mu.Unlock()
		})
	}
	wg.Wait()
	return out
}

func connect(ctx context.Context, addr string, timeout time.Duration) string {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	var timedOut interface{ Timeout() bool }
	switch {
	case err == nil:
		_ = conn.Close()
		return Connected
	case errors.Is(err, syscall.ECONNREFUSED):
		return Refused
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return NoRoute
	case errors.As(err, &timedOut) && timedOut.Timeout():
		return TimedOut
	}
	return "failed: " + err.Error()
}

// Resolve looks each name up, each within timeout, and returns the ones
// that have an address.
func Resolve(ctx context.Context, names []string, timeout time.Duration) []string {
	var out []string
	for _, name := range names {
		ctx, cancel := context.WithTimeout(ctx, timeout)
		addrs, err := net.DefaultResolver.LookupHost(ctx, name)
		cancel()
		if err == nil && len(addrs) > 0 {
			out = append(out, name)
		}
	}
	return out
}
