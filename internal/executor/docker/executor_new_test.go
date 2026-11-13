package docker

import (
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/executor/executortest"
)

// An Executor is not made on a daemon that is too old, that lacks the
// runtime asked for, or that has no gateway container.
func TestNewExecutorRefuses(t *testing.T) {
	api := MinAPI
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			reply(w, http.StatusOK, map[string]string{"Version": "29.0", "ApiVersion": api})
		case strings.HasSuffix(r.URL.Path, "/info"):
			reply(w, http.StatusOK, map[string]any{"Runtimes": map[string]any{"runc": nil}})
		case strings.HasSuffix(r.URL.Path, "/containers/gw/json"):
			reply(w, http.StatusOK, map[string]string{"Id": "gw"})
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			reply(w, http.StatusOK, []any{})
		default:
			fail(w, http.StatusNotFound, "No such container")
		}
	})
	good := Options{Client: c, Secrets: &executortest.Secrets{}, Gateway: "gw", RunnerImage: "example.com/javo:1"}
	x, err := NewExecutor(t.Context(), good)
	if err != nil {
		t.Fatalf("a daemon that has it all: %v", err)
	}
	x.Close()

	for want, change := range map[string]func(*Options){
		"no runtime \"runsc\"": func(o *Options) { o.Runtime = "runsc" },
		"gateway container":    func(o *Options) { o.Gateway = "absent" },
		"no runner image":      func(o *Options) { o.RunnerImage = "" },
		"no secrets":           func(o *Options) { o.Secrets = nil },
		"no gateway container": func(o *Options) { o.Gateway = "" },
		"no client":            func(o *Options) { o.Client = nil },
		"the proxy must be":    func(o *Options) { o.Proxy = "javo:3128" },
	} {
		o := good
		change(&o)
		if _, err := NewExecutor(t.Context(), o); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want an error about %q, got %v", want, err)
		}
	}
	api = "1.40"
	if _, err := NewExecutor(t.Context(), good); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Errorf("an old daemon: %v", err)
	}
}
