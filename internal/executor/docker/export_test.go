package docker

import (
	"context"
	"net"
	"time"
)

// What the daemon tests of the parts built on this package (homes and the
// file helper, in homes_daemon_test.go) need of its insides. They are in
// a test package of their own, since those parts import this package.

// TestImage is the image of the daemon tests.
const TestImage = testImage

// TestPool is where the networks of this test process take their
// addresses.
var TestPool = testPool

// TestPause stands in for the runner image's "javo pause".
var TestPause = testPause

// DialThrough connects from inside the container gw, as Javo does from
// the gateway container.
func DialThrough(c *Client, gw string) func(context.Context, string, string) (net.Conn, error) {
	return dialThrough(c, gw)
}

// NewTestExecutor is NewExecutor with a sandbox that needs no runner
// image and readiness checks made from inside the gateway container gw.
func NewTestExecutor(ctx context.Context, o Options) (*Executor, error) {
	return build(ctx, o, internals{pause: testPause, dial: dialThrough(o.Client, o.Gateway), every: 100 * time.Millisecond})
}
