package docker

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"
)

// sweepEnv asks for what test processes that were killed left on the
// daemon to be removed: it holds how old a thing must be to go ("2h").
// Nothing else removes another test process's things, and a process that
// is still running against the same daemon loses the ones that old.
const sweepEnv = "JAVO_TEST_SWEEP_OLDER_THAN"

// leftovers are the things carrying testLabel, whatever its value, that
// were made before before, as the paths that delete them.
func leftovers(ctx context.Context, c *Client, before time.Time) ([]string, error) {
	var paths []string
	filter := labelFilter([]string{testLabel}, nil)
	all := url.Values{"all": {"1"}, "filters": filter["filters"]}
	var containers []struct {
		ID      string `json:"Id"`
		Created int64
	}
	if err := c.get(ctx, "/containers/json", all, &containers); err != nil {
		return nil, err
	}
	for _, x := range containers {
		if time.Unix(x.Created, 0).Before(before) {
			paths = append(paths, "/containers/"+x.ID+"?force=1&v=1")
		}
	}
	var networks []struct {
		ID      string `json:"Id"`
		Created time.Time
	}
	if err := c.get(ctx, "/networks", filter, &networks); err != nil {
		return nil, err
	}
	for _, x := range networks {
		if x.Created.Before(before) {
			paths = append(paths, "/networks/"+x.ID)
		}
	}
	var volumes struct {
		Volumes []struct {
			Name      string
			CreatedAt time.Time
		}
	}
	if err := c.get(ctx, "/volumes", filter, &volumes); err != nil {
		return nil, err
	}
	for _, x := range volumes.Volumes {
		if x.CreatedAt.Before(before) {
			paths = append(paths, "/volumes/"+url.PathEscape(x.Name))
		}
	}
	return paths, nil
}

// Run on purpose, with sweepEnv set, it removes what earlier test
// processes left on the daemon and did not sweep themselves.
func TestDaemonSweepLeftovers(t *testing.T) {
	age, err := time.ParseDuration(os.Getenv(sweepEnv))
	if err != nil || age <= 0 {
		t.Skipf("set %s to an age (such as 2h) to remove what killed test runs left on the daemon", sweepEnv)
	}
	c, _ := daemon(t)
	ctx := testContext(t)
	paths, err := leftovers(ctx, c, time.Now().Add(-age))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if err := gone(c.call(ctx, request{method: http.MethodDelete, path: p}, nil)); err != nil {
			t.Errorf("removing %s: %v", p, err)
		}
	}
	t.Logf("removed %d things older than %s", len(paths), age)
}
