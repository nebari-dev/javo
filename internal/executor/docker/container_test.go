package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// calls records what a fake daemon was asked.
type calls struct {
	mu   sync.Mutex
	list []string
}

func (c *calls) add(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, r.Method+" "+strings.TrimPrefix(r.URL.RequestURI(), "/v"+MinAPI))
}

func (c *calls) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.list...)
}

func TestCreateSendsTheBodyUnderTheName(t *testing.T) {
	var got Body
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/v"+MinAPI+"/containers/create" || r.URL.Query().Get("name") != "javo-a.b.app" {
			fail(w, 400, "unexpected "+r.URL.String())
			return
		}
		json.NewDecoder(r.Body).Decode(&got)
		reply(w, 201, map[string]string{"Id": "abc"})
	})
	s := spec()
	id, err := c.ContainerCreate(context.Background(), "javo-a.b.app", mustBody(t, s, s.Main))
	if err != nil || id != "abc" {
		t.Fatalf("id %q, err %v", id, err)
	}
	if got.User != "1000:100" || got.HostConfig.Memory != 1<<30 {
		t.Fatalf("daemon got %+v", got)
	}
}

func TestStartingARunningAndStoppingAStoppedContainerIsFine(t *testing.T) {
	var seen calls
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		seen.add(r)
		w.WriteHeader(http.StatusNotModified)
	})
	ctx := context.Background()
	if err := c.ContainerStart(ctx, "abc"); err != nil {
		t.Fatal(err)
	}
	if err := c.ContainerStop(ctx, "abc", 10*time.Second); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /containers/abc/start", "POST /containers/abc/stop?t=10"}
	if got := seen.all(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls = %v", got)
	}
}

func TestWaitReturnsTheExitCode(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("condition") != "not-running" {
			fail(w, 400, "no condition")
			return
		}
		reply(w, 200, map[string]int{"StatusCode": 3})
	})
	if code, err := c.ContainerWait(context.Background(), "abc"); err != nil || code != 3 {
		t.Fatalf("code %d, err %v", code, err)
	}
}

// Nothing is removed that does not carry Javo's label, whatever asks.
func TestOnlyJavosContainersAreRemoved(t *testing.T) {
	var seen calls
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		seen.add(r)
		name := strings.Split(r.URL.Path, "/")[3]
		switch {
		case name == "gone":
			fail(w, 404, "no such container")
		case r.Method == "GET":
			labels := map[string]string{}
			if name == "ours" {
				labels[LabelManaged] = "true"
			}
			reply(w, 200, map[string]any{"Id": "id-" + name, "Config": map[string]any{"Labels": labels}})
		default:
			w.WriteHeader(204)
		}
	})
	ctx := context.Background()
	if err := c.ContainerRemove(ctx, "theirs"); !errors.Is(err, ErrForeign) {
		t.Fatalf("foreign: %v", err)
	}
	if err := c.ContainerRemove(ctx, "gone"); err != nil {
		t.Fatalf("gone: %v", err)
	}
	if err := c.ContainerRemove(ctx, "ours"); err != nil {
		t.Fatalf("ours: %v", err)
	}
	var deletes []string
	for _, call := range seen.all() {
		if strings.HasPrefix(call, "DELETE") {
			deletes = append(deletes, call)
		}
	}
	if len(deletes) != 1 || deletes[0] != "DELETE /containers/id-ours?force=1&v=1" {
		t.Fatalf("deletes = %v", deletes)
	}
}

func TestListFiltersByLabel(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("all") != "1" || q.Get("filters") != `{"label":["javo.managed=true","javo.name=x"]}` {
			fail(w, 400, "filters "+q.Get("filters"))
			return
		}
		reply(w, 200, []map[string]any{{"Id": "abc", "State": "running", "Labels": map[string]string{LabelName: "x"}}})
	})
	got, err := c.ContainerList(context.Background(), Managed, LabelName+"=x")
	if err != nil || len(got) != 1 || got[0].ID != "abc" || got[0].Labels[LabelName] != "x" {
		t.Fatalf("list %+v, err %v", got, err)
	}
}

func TestExtractSendsTheTarAndPathExistsAsks(t *testing.T) {
	c := fake(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "PUT" && r.URL.Query().Get("path") == "/v":
			raw, _ := io.ReadAll(r.Body)
			if string(raw) != "tar bytes" || r.Header.Get("Content-Type") != "application/x-tar" {
				fail(w, 400, "body")
			}
		case r.Method == "HEAD" && r.URL.Query().Get("path") == "/v/there":
		default:
			w.WriteHeader(404)
		}
	})
	ctx := context.Background()
	if err := c.Extract(ctx, "abc", "/v", strings.NewReader("tar bytes")); err != nil {
		t.Fatal(err)
	}
	if ok, err := c.PathExists(ctx, "abc", "/v/there"); err != nil || !ok {
		t.Fatalf("there: %v %v", ok, err)
	}
	if ok, err := c.PathExists(ctx, "abc", "/v/nope"); err != nil || ok {
		t.Fatalf("nope: %v %v", ok, err)
	}
}
