package docker

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
)

// Exec asks for a command in a container with nothing but the command and
// its output, waits for it to end and returns its exit code.
func TestExecRunsACommandAndReturnsItsExitCode(t *testing.T) {
	var created map[string]any
	var calls []string
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.URL.Path {
		case "/v" + MinAPI + "/containers/c1/exec":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &created)
			reply(w, http.StatusCreated, map[string]string{"Id": "e1"})
		case "/v" + MinAPI + "/exec/e1/start":
			_, _ = w.Write([]byte("output nobody reads"))
		case "/v" + MinAPI + "/exec/e1/json":
			reply(w, http.StatusOK, map[string]any{"ExitCode": 3, "Running": false})
		default:
			fail(w, http.StatusNotFound, "no such page")
		}
	})
	code, err := c.Exec(t.Context(), "c1", []string{"test", "-e", "/ready"})
	if err != nil || code != 3 {
		t.Fatalf("Exec = %d, %v", code, err)
	}
	if len(calls) != 3 {
		t.Fatalf("calls %v", calls)
	}
	// No user, no privileges, no variables, no terminal: the command runs
	// as the container does.
	want := map[string]any{"AttachStdout": true, "AttachStderr": true, "Cmd": []any{"test", "-e", "/ready"}}
	if len(created) != len(want) || created["AttachStdout"] != true || created["AttachStderr"] != true || len(created["Cmd"].([]any)) != 3 {
		t.Fatalf("exec created from %v, want %v", created, want)
	}
}

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
