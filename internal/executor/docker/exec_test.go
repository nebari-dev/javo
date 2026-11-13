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
