package supervise

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/nebari-dev/javo/internal/workload"
)

// checkTimeout is how long one try of a readiness check may take.
const checkTimeout = time.Second

// answers waits until u's readiness check passes; false when ctx ended
// first. A check that names its Tries and has failed that many times is a
// failure of the unit, and is tried on: the failure goes once it passes.
// A unit with no check is ready as soon as it runs. Once passed, a check
// is not tried again.
func (s *Supervisor) answers(ctx context.Context, w *work, u workload.Unit) bool {
	for n := 1; !s.check(ctx, w, u); n++ {
		if n == int(u.Ready.Tries) {
			s.update(w, func() {
				w.failed[u.Name] = &failure{what: fmt.Sprintf("unit %s did not pass its check in %d tries", u.Name, n), exit: -1}
			})
		}
		if !sleep(ctx, s.every) {
			return false
		}
	}
	s.update(w, func() { delete(w.failed, u.Name) })
	return true
}

// check tries u's readiness check once.
func (s *Supervisor) check(ctx context.Context, w *work, u workload.Unit) bool {
	c := u.Ready
	switch {
	case c == nil:
		return true
	case len(c.Exec) > 0:
		// Run beside the unit, with what the unit sees.
		probe := workload.Unit{
			Name: u.Name + "-check", Image: u.Image, Command: c.Exec,
			Dir: u.Dir, Env: u.Env, Mounts: u.Mounts, ReadOnlyRoot: true,
		}
		r, err := s.eng.Start(ctx, w.spec, probe)
		if err != nil {
			return false
		}
		code, ok := wait(ctx, r)
		return ok && code == 0
	}
	addr, err := s.eng.Addr(ctx, w.spec.Ref, w.spec.Port)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	if c.TCP {
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		if err != nil {
			return false
		}
		_ = conn.Close()
		return true
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+c.HTTPPath, nil)
	if err != nil {
		return false
	}
	resp, err := s.probe.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 400
}

// checkWords is the main unit's check as a person reads it.
func checkWords(s workload.Spec) string {
	switch c := s.Main.Ready; {
	case c == nil:
		return "(none configured)"
	case len(c.Exec) > 0:
		return "run " + c.Exec[0]
	case c.TCP:
		return fmt.Sprintf("TCP :%d", s.Port)
	default:
		return fmt.Sprintf("GET :%d%s", s.Port, c.HTTPPath)
	}
}
