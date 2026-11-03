package apps_test

import (
	"net/http"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
)

func TestCreateWithIdleStop(t *testing.T) {
	f := apitest.New(t, apitest.Options{Idle: apitest.StrictIdle()}, gatewaytest.Template("lab", "jupyterlab"))
	bob := f.Session("bob-bbbbbbbb")
	for _, c := range []struct {
		name, body string
		want       int32
	}{
		{"default", `{"displayName":"Plain","template":"lab"}`, 60},
		{"asked", `{"displayName":"Short","template":"lab","idleStopMinutes":30}`, 30},
	} {
		w := f.Do("POST", "/api/workspaces", c.body, bob)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s: status %d: %s", c.name, w.Code, w.Body)
		}
		if got := apitest.Decode[apitest.IdleJSON](t, w).IdleStopMinutes; got != c.want {
			t.Fatalf("%s: idleStopMinutes %d, want %d", c.name, got, c.want)
		}
	}
	if got := f.GetWorkspace("bob-bbbbbbbb", "short").IdleStopMinutes; got != 30 {
		t.Fatalf("stored %d", got)
	}
}

func TestCreateRefusesAnIdleStopThePolicyForbids(t *testing.T) {
	f := apitest.New(t, apitest.Options{Idle: apitest.StrictIdle()}, gatewaytest.Template("lab", "jupyterlab"))
	bob := f.Session("bob-bbbbbbbb")
	for _, c := range []struct{ minutes, want string }{
		{"5", "Pick between 10 minutes and 2 hours."},
		{"121", "Pick between 10 minutes and 2 hours."},
		{"0", "Apps here must stop when idle. Pick how long they may sit unused."},
	} {
		w := f.Do("POST", "/api/workspaces", `{"displayName":"Lab","template":"lab","idleStopMinutes":`+c.minutes+`}`, bob)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status %d: %s", c.minutes, w.Code, w.Body)
		}
		if got, _ := apitest.Decode[apitest.Problem](t, w).FieldMessage("body.idleStopMinutes"); got != c.want {
			t.Fatalf("%s: message %q, want %q", c.minutes, got, c.want)
		}
	}
	if f.WorkspaceCount() != 0 {
		t.Fatal("created")
	}
}

// Platform admins may turn the stop off even when people may not.
func TestAdminMayTurnIdleStopOff(t *testing.T) {
	f := apitest.New(t, apitest.Options{Idle: apitest.StrictIdle()}, gatewaytest.Template("lab", "jupyterlab"))
	admin := f.Session("alice-aaaaaaaa", "javo-admins")
	w := f.Do("POST", "/api/workspaces", `{"displayName":"Lab","template":"lab","idleStopMinutes":0}`, admin)
	if w.Code != http.StatusCreated || apitest.Decode[apitest.IdleJSON](t, w).IdleStopMinutes != 0 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestChangingIdleStop(t *testing.T) {
	ws := gatewaytest.Workspace("lab", "bob-bbbbbbbb", "Lab")
	f := apitest.New(t, apitest.Options{Idle: apitest.StrictIdle()}, gatewaytest.Template("streamlit", "streamlit"), ws)
	bob := f.Session("bob-bbbbbbbb")
	w := f.Do("PUT", "/api/workspaces/lab/idle-stop", `{"idleStopMinutes":90}`, bob)
	if w.Code != http.StatusOK || apitest.Decode[apitest.IdleJSON](t, w).IdleStopMinutes != 90 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if got := f.GetWorkspace("bob-bbbbbbbb", "lab").IdleStopMinutes; got != 90 {
		t.Fatalf("stored %d", got)
	}
	w = f.Do("PUT", "/api/workspaces/lab/idle-stop", `{"idleStopMinutes":0}`, bob)
	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("turned off against the policy: %d %s", w.Code, w.Body)
	}
	if w := f.Do("PUT", "/api/workspaces/lab/idle-stop", `{"idleStopMinutes":30}`, f.Session("carol-cccccccc")); w.Code != http.StatusNotFound {
		t.Fatalf("someone else's app: %d", w.Code)
	}
}

// The UI reads the policy as it applies to the caller.
func TestIdleStopPolicy(t *testing.T) {
	f := apitest.New(t, apitest.Options{Idle: apitest.StrictIdle()})
	type policyJSON struct {
		DefaultMinutes, MinMinutes, MaxMinutes int32
		CanTurnOff                             bool
	}
	w := f.Do("GET", "/api/idle-stop", "", f.Session("bob-bbbbbbbb"))
	got := apitest.Decode[policyJSON](t, w)
	if w.Code != http.StatusOK || got != (policyJSON{60, 10, 120, false}) {
		t.Fatalf("member: %d %+v", w.Code, got)
	}
	if got := apitest.Decode[policyJSON](t, f.Do("GET", "/api/idle-stop", "", f.Session("alice-aaaaaaaa", "javo-admins"))); !got.CanTurnOff {
		t.Fatalf("admin: %+v", got)
	}
}

// An app the idle check stopped says so until it is started again.
func TestStartClearsTheIdleStop(t *testing.T) {
	ws := gatewaytest.Workspace("lab", "bob-bbbbbbbb", "Lab")
	ws.Paused, ws.IdleStopMinutes, ws.StoppedIdleAfter = true, 60, 60
	f := apitest.New(t, apitest.Options{Idle: apitest.StrictIdle()}, gatewaytest.Template("streamlit", "streamlit"), ws)
	bob := f.Session("bob-bbbbbbbb")
	if got := apitest.Decode[apitest.IdleJSON](t, f.Do("GET", "/api/workspaces/lab", "", bob)).StoppedIdleAfter; got != 60 {
		t.Fatalf("stopped app: stoppedIdleAfter %d", got)
	}
	w := f.Do("POST", "/api/workspaces/lab/start", "", bob)
	if w.Code != http.StatusOK || apitest.Decode[apitest.IdleJSON](t, w).StoppedIdleAfter != 0 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
}
