package docker

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestDaemonReportsWhatRuns(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			reply(w, 200, map[string]string{"Version": "29.2.1", "ApiVersion": "1.53"})
		case "/v" + MinAPI + "/info":
			reply(w, 200, map[string]any{
				"SecurityOptions": []string{"name=seccomp,profile=builtin", "name=rootless"},
				"Runtimes":        map[string]any{"runc": map[string]any{}, "runsc": map[string]any{}},
				"CgroupVersion":   "2",
			})
		default:
			fail(w, 404, "page not found")
		}
	})
	d, err := c.Daemon(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if d.Version != "29.2.1" || d.APIVersion != "1.53" || !d.Rootless || !d.HasRuntime("runsc") || d.HasRuntime("kata") {
		t.Fatalf("daemon = %+v", d)
	}
}

func TestDaemonRefusesAnOldAPI(t *testing.T) {
	for _, v := range []string{"1.47", "1.9", "", "x"} {
		c := fake(t, func(w http.ResponseWriter, r *http.Request) {
			reply(w, 200, map[string]string{"Version": "27.0.0", "ApiVersion": v})
		})
		if _, err := c.Daemon(context.Background()); !errors.Is(err, ErrTooOld) {
			t.Fatalf("API %q: err = %v, want ErrTooOld", v, err)
		}
	}
}

func TestErrorsAreTyped(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/gone/json"):
			fail(w, 404, "No such container: gone")
		case strings.HasSuffix(r.URL.Path, "/taken/json"):
			fail(w, 409, "name is in use")
		default:
			fail(w, 500, "boom")
		}
	})
	ctx := context.Background()
	if _, err := c.ContainerInspect(ctx, "gone"); !errors.Is(err, ErrNotFound) || !strings.Contains(err.Error(), "No such container") {
		t.Fatalf("404: %v", err)
	}
	if _, err := c.ContainerInspect(ctx, "taken"); !errors.Is(err, ErrConflict) {
		t.Fatalf("409: %v", err)
	}
	_, err := c.ContainerInspect(ctx, "other")
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("500: %v", err)
	}
}

func TestARequestEndsWithItsContext(t *testing.T) {
	release := make(chan struct{})
	c := fake(t, func(w http.ResponseWriter, r *http.Request) { <-release })
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ContainerInspect(ctx, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}
