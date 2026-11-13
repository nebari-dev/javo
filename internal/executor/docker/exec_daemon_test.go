//go:build e2e

package docker

import "testing"

func TestDaemonExecRunsInTheContainer(t *testing.T) {
	c, _ := daemon(t)
	ctx := testContext(t)
	s := testSpec("exec")
	up(t, c, s)
	id := start(t, c, s, sh("app", "touch /tmp/mine; sleep 3600"))
	// Only the container itself has /tmp/mine and its user.
	code, err := c.Exec(ctx, id, []string{"sh", "-c", `until [ -e /tmp/mine ]; do sleep 0.1; done; [ "$(id -u)" = 1000 ] && exit 7`})
	if err != nil || code != 7 {
		t.Fatalf("Exec = %d, %v", code, err)
	}
	if code, err := c.Exec(ctx, id, []string{"true"}); err != nil || code != 0 {
		t.Fatalf("Exec of true = %d, %v", code, err)
	}
}
