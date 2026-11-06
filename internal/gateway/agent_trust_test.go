package gateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/auth/token"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/apps"
	"github.com/nebari-dev/javo/internal/model"
)

// trustFixture is the agent fixture on a clock the test moves.
type trustFixture struct {
	agentFixture
	clock *apitest.TestClock
}

func newTrustFixture(t *testing.T, o apitest.Options) trustFixture {
	t.Helper()
	clock := &apitest.TestClock{T: time.Now().UTC().Truncate(time.Second)}
	o.Now = clock.Now
	return trustFixture{agentFixture: newAgentFixtureWith(t, true, o), clock: clock}
}

type trustJSON struct {
	Trust *struct {
		Allowed bool       `json:"allowed"`
		Until   *time.Time `json:"until"`
	} `json:"trust"`
}

// trust turns the pi app's trust on for hours with the session c.
func (f trustFixture) trust(c *http.Cookie, hours int) *trustJSON {
	f.T.Helper()
	w := f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":true,"hours":`+strconv.Itoa(hours)+`}`, c)
	wantCode(f.T, "turn trust on", w, http.StatusOK)
	v := apitest.Decode[trustJSON](f.T, w)
	return &v
}

// attachDirectly gives the plain app the secret API_KEY with the agent's
// token, the call an untrusted agent may only propose.
func (f trustFixture) attachDirectly() *httptest.ResponseRecorder {
	return f.agent("PUT", "/api/workspaces/plain/env", `{"env":[{"name":"K","secret":"API_KEY"}]}`)
}

// Only the app's owner, signed in in the browser, lets the agent act
// without asking: no token, not even the owner's own, and not an admin on
// someone else's app.
func TestOnlyTheOwnersSessionTrustsAnAgent(t *testing.T) {
	f := newTrustFixture(t, apitest.Options{})
	personal, _, err := f.Tokens.Create(context.Background(), model.Actor{Person: apitest.LocalAlice()}, token.Token{
		Name: "laptop", Owner: apitest.LocalAlice(), Scope: token.ScopeReadWrite,
	})
	if err != nil {
		t.Fatal(err)
	}
	on := `{"trusted":true,"hours":8}`
	wantCode(t, "alice's personal token", f.Bearer("PUT", "/api/workspaces/pi/trust", on, personal, nil), http.StatusForbidden)
	wantCode(t, "the agent's own token", f.agent("PUT", "/api/workspaces/pi/trust", on), http.StatusForbidden)
	bob := f.Session("bob-0000")
	wantCode(t, "bob", f.Do("PUT", "/api/workspaces/pi/trust?owner="+apitest.LocalAlice(), on, bob), http.StatusNotFound)
	if app := f.GetWorkspace(apitest.LocalAlice(), "pi"); !app.TrustedUntil.IsZero() {
		t.Fatalf("trusted after refusals: %v", app.TrustedUntil)
	}
	for _, h := range []int{0, 25} {
		wantCode(t, "hours "+strconv.Itoa(h), f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":true,"hours":`+strconv.Itoa(h)+`}`, f.alice), http.StatusUnprocessableEntity)
	}

	got := f.trust(f.alice, 8)
	want := f.clock.Now().Add(8 * time.Hour)
	if got.Trust == nil || !got.Trust.Allowed || got.Trust.Until == nil || !got.Trust.Until.Equal(want) {
		t.Fatalf("trust %+v, want until %v", got.Trust, want)
	}
	// Leaving the hours out is 8 hours.
	w := f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":true}`, f.alice)
	wantCode(t, "default hours", w, http.StatusOK)
	if v := apitest.Decode[trustJSON](t, w); v.Trust.Until == nil || !v.Trust.Until.Equal(want) {
		t.Fatalf("default hours: %+v", v.Trust)
	}
	// An admin may not trust someone else's agent: it acts as its owner.
	f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":false}`, f.alice)
	bobsPi := *f.GetWorkspace(apitest.LocalAlice(), "pi")
	bobsPi.ID, bobsPi.Owner = "", "bob-0000"
	if _, err := f.Db.Apps().Create(context.Background(), model.Actor{Person: "bob-0000"}, &bobsPi); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "admin alice on bob's agent", f.Do("PUT", "/api/workspaces/pi/trust?owner=bob-0000", on, f.alice), http.StatusForbidden)
}

// A trusted agent does what its owner may, directly; it still never
// decides a plan, changes trust or makes a token.
func TestTrustedAgentActsWithoutAsking(t *testing.T) {
	f := newTrustFixture(t, apitest.Options{})
	wantCode(t, "untrusted attach", f.attachDirectly(), http.StatusForbidden)
	f.trust(f.alice, 8)
	wantCode(t, "trusted attach", f.attachDirectly(), http.StatusOK)
	if b := f.bindings("plain"); len(b) != 1 || b[0].Secret != "API_KEY" {
		t.Fatalf("bindings %+v", b)
	}

	w := f.agent("POST", "/api/plans", `{"source":"rules","proposal":{"action":"restart","target":"keyed"}}`)
	wantCode(t, "propose", w, http.StatusCreated)
	id := apitest.Decode[apitest.PlanJSON](t, w).ID
	for _, verb := range []string{"approve", "reject"} {
		wantCode(t, "trusted "+verb, f.agent("POST", "/api/plans/"+id+"/"+verb, ""), http.StatusForbidden)
	}
	wantCode(t, "trusted turns trust off", f.agent("PUT", "/api/workspaces/pi/trust", `{"trusted":false}`), http.StatusForbidden)
	wantCode(t, "trusted renews trust", f.agent("PUT", "/api/workspaces/pi/trust", `{"trusted":true,"hours":24}`), http.StatusForbidden)
	wantCode(t, "trusted mints a token", f.agent("POST", "/api/tokens", `{"name":"x","scope":"read-write","expiresInDays":1}`), http.StatusForbidden)
	wantCode(t, "trusted acts as an admin", f.agent("GET", "/api/admin/audit", ""), http.StatusForbidden)

	// The audit log names the agent, acting as trusted.
	evs, err := f.Db.Audit().List(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	pi := f.GetWorkspace(apitest.LocalAlice(), "pi")
	var on, acted bool
	for _, e := range evs {
		switch {
		case e.Action == "app.trust.on" && e.ObjectName == "pi" && e.Actor == apitest.LocalAlice() && e.Via == "session":
			on = true
		case e.ObjectName == "plain" && e.Actor == "app:"+pi.ObjectName() && e.Via == "trusted":
			acted = true
		}
	}
	if !on || !acted {
		t.Fatalf("audit: trust on %v, trusted action %v: %+v", on, acted, evs)
	}

	// Turned off in the browser, the agent asks again at once.
	w = f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":false}`, f.alice)
	wantCode(t, "turn off", w, http.StatusOK)
	if v := apitest.Decode[trustJSON](t, w); v.Trust == nil || v.Trust.Until != nil {
		t.Fatalf("after off: %+v", v.Trust)
	}
	wantCode(t, "attach after off", f.agent("PUT", "/api/workspaces/keyed/env", `{"env":[]}`), http.StatusForbidden)
}

// At its end the agent asks again, before the janitor clears it; the
// janitor records the end.
func TestTrustExpires(t *testing.T) {
	f := newTrustFixture(t, apitest.Options{})
	f.trust(f.alice, 1)
	f.clock.Add(time.Hour)
	wantCode(t, "attach after the hour", f.attachDirectly(), http.StatusForbidden)
	j := apps.TrustJanitor{DB: f.Db, Policy: apps.TrustPolicy{Config: true, DB: f.Db}, Now: f.clock.Now}
	if n, err := j.Sweep(context.Background()); err != nil || n != 1 {
		t.Fatalf("sweep: %d, %v", n, err)
	}
	if app := f.GetWorkspace(apitest.LocalAlice(), "pi"); !app.TrustedUntil.IsZero() {
		t.Fatalf("still trusted until %v", app.TrustedUntil)
	}
	if n, err := j.Sweep(context.Background()); err != nil || n != 0 {
		t.Fatalf("second sweep: %d, %v", n, err)
	}
	evs, _ := f.Db.Audit().List(context.Background(), 50)
	found := false
	for _, e := range evs {
		found = found || e.Action == "app.trust.expire" && e.ObjectName == "pi" && e.Actor == model.SystemGateway
	}
	if !found {
		t.Fatal("no app.trust.expire audit row")
	}
}

// The platform admin's switch refuses turning it on and ends it where it is on.
func TestAdminSwitchEndsTrust(t *testing.T) {
	f := newTrustFixture(t, apitest.Options{})
	f.trust(f.alice, 8)
	bob := f.Session("bob-0000")
	wantCode(t, "a member sets the switch", f.Do("PUT", "/api/admin/agents", `{"allowTrusted":false}`, bob), http.StatusForbidden)
	wantCode(t, "admin turns it off", f.Do("PUT", "/api/admin/agents", `{"allowTrusted":false}`, f.alice), http.StatusOK)
	wantCode(t, "attach after the switch", f.attachDirectly(), http.StatusForbidden)
	if app := f.GetWorkspace(apitest.LocalAlice(), "pi"); !app.TrustedUntil.IsZero() {
		t.Fatalf("still trusted until %v", app.TrustedUntil)
	}
	wantCode(t, "turn on while off", f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":true}`, f.alice), http.StatusForbidden)
	// A trust end written while the switch is off does not count either.
	pi := f.GetWorkspace(apitest.LocalAlice(), "pi")
	if _, err := f.Db.Apps().SetTrust(context.Background(), model.Actor{Person: apitest.LocalAlice()}, pi.ID, f.clock.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	wantCode(t, "attach with a stray trust end", f.attachDirectly(), http.StatusForbidden)
	w := f.Do("GET", "/api/workspaces/pi", "", f.alice)
	if v := apitest.Decode[trustJSON](t, w); v.Trust == nil || v.Trust.Allowed {
		t.Fatalf("view while off: %+v", v.Trust)
	}
	w = f.Do("GET", "/api/admin/agents", "", f.alice)
	wantCode(t, "read the switch", w, http.StatusOK)
	if v := apitest.Decode[struct{ AllowTrusted, ConfigAllows bool }](t, w); v.AllowTrusted || !v.ConfigAllows {
		t.Fatalf("switch %+v", v)
	}
	wantCode(t, "admin turns it back on", f.Do("PUT", "/api/admin/agents", `{"allowTrusted":true}`, f.alice), http.StatusOK)
	f.trust(f.alice, 8)
}

// agents.allowTrusted false in the platform config refuses it everywhere.
func TestConfigRefusesTrust(t *testing.T) {
	f := newTrustFixture(t, apitest.Options{NoTrustedAgents: true})
	wantCode(t, "turn on", f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":true}`, f.alice), http.StatusForbidden)
	wantCode(t, "admin turns the switch on", f.Do("PUT", "/api/admin/agents", `{"allowTrusted":true}`, f.alice), http.StatusOK)
	wantCode(t, "turn on with the switch on", f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":true}`, f.alice), http.StatusForbidden)
}

// Only a coding agent with read-write API access can be trusted.
func TestOnlyAgentsCanBeTrusted(t *testing.T) {
	f := newTrustFixture(t, apitest.Options{})
	wantCode(t, "a streamlit app", f.Do("PUT", "/api/workspaces/plain/trust", `{"trusted":true}`, f.alice), http.StatusUnprocessableEntity)
	if v := apitest.Decode[trustJSON](t, f.Do("GET", "/api/workspaces/plain", "", f.alice)); v.Trust != nil {
		t.Fatalf("a streamlit app shows trust: %+v", v.Trust)
	}
	wantCode(t, "read-only agent", f.Do("PUT", "/api/workspaces/pi/api-access", `{"apiAccess":"Read"}`, f.alice), http.StatusOK)
	wantCode(t, "trust a read-only agent", f.Do("PUT", "/api/workspaces/pi/trust", `{"trusted":true}`, f.alice), http.StatusUnprocessableEntity)
}
