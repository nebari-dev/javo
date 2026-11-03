package apitest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
	"github.com/nebari-dev/javo/internal/model"
)

type PlanJSON struct {
	ID, Source, State, Error string
	Planned                  struct {
		Action, Target, Summary string
		Changes                 []struct{ Field, From, To string }
	}
	ExpiresAt time.Time
}

// DoWith is do with extra request headers.
func (f *Fixture) DoWith(method, path, body string, c *http.Cookie, h map[string]string) *httptest.ResponseRecorder {
	f.T.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range h {
		r.Header.Set(k, v)
	}
	if c != nil {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	f.Srv.ServeHTTP(w, r)
	return w
}

// NewPlan proposes action on alice's app sales from the rules and returns the plan.
func NewPlan(t *testing.T, f *Fixture, alice string, action string) PlanJSON {
	t.Helper()
	w := f.Do("POST", "/api/plans", `{"source":"rules","proposal":{"action":"`+action+`","target":"sales"}}`, f.Session(alice))
	if w.Code != http.StatusCreated {
		t.Fatalf("create plan: %d %s", w.Code, w.Body)
	}
	return Decode[PlanJSON](t, w)
}

const PlanOwner = "alice-aaaaaaaa"

func RunningSales() *model.App {
	app := gatewaytest.Workspace("sales", PlanOwner, "Sales")
	app.Status.Phase = "Running"
	return app
}

func RunningSalesOf(owner string) *model.App {
	app := gatewaytest.Workspace("sales", owner, "Sales")
	app.Status.Phase = "Running"
	return app
}
