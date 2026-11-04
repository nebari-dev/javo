package apps_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/ai/aitest"
	"github.com/nebari-dev/javo/internal/ai/redact"
	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/gatewaytest"
)

const alicePassword = "Vx9-kQ2pLr7mWz4s"

// secretsFixture is alice's crashed app whose log holds candidate values,
// one of them her stored secret DB_PASSWORD, with AI on.
func secretsFixture(t *testing.T, replies ...aitest.Reply) (*apitest.Fixture, *aitest.Scripted) {
	t.Helper()
	scan, err := redact.NewScanner()
	if err != nil {
		t.Fatal(err)
	}
	p := aitest.New(replies...)
	svc := apitest.AiService(p)
	svc.Redactor = scan
	svc.SecretsOf = func(_ context.Context, owner string) (func(string) []redact.Span, bool, error) {
		return func(text string) []redact.Span {
			if owner != apitest.PlanOwner {
				return nil
			}
			var out []redact.Span
			for i := 0; ; {
				j := strings.Index(text[i:], alicePassword)
				if j < 0 {
					return out
				}
				out = append(out, redact.Span{Start: i + j, End: i + j + len(alicePassword), Name: "DB_PASSWORD"})
				i += j + len(alicePassword)
			}
		}, false, nil
	}
	logs := &apitest.RunLogs{Cur: "2026-10-30T12:01:00Z GET /?q=guess-one-aaaa\n" +
		"2026-10-30T12:01:01Z GET /?q=" + alicePassword + "\n" +
		"2026-10-30T12:01:02Z GET /?q=guess-three-cc\n"}
	pod := apitest.Running(gatewaytest.LivePod(apitest.PlanOwner, "sales", "uid-live"), 0)
	f := apitest.New(t, apitest.Options{Ai: svc, Logs: logs}, apitest.CrashedApp(), gatewaytest.Template("streamlit", "streamlit"), pod)
	return f, p
}

// Someone other than the owner (a platform admin) asking to explain an app
// does not get what is sent, and nothing they see names a secret or says
// which line held one: a preview cannot be used to test guessed values.
func TestExplainBundleIsShownOnlyToTheOwner(t *testing.T) {
	t.Parallel()
	f, _ := secretsFixture(t)
	admin := f.Session(rootAdmin, "javo-admins")
	w := f.Do("POST", "/api/workspaces/sales/diagnosis/explain?owner="+apitest.PlanOwner, `{"preview":true}`, admin)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	body := w.Body.String()
	if strings.Contains(body, "DB_PASSWORD") || strings.Contains(body, "guess-one") || strings.Contains(body, "redacted") {
		t.Fatalf("an admin's preview shows the bundle: %s", body)
	}
	if r := apitest.Decode[struct {
		Bundle struct {
			Text   string
			Hidden int
		}
	}](t, w); r.Bundle.Text != "" || r.Bundle.Hidden != 0 {
		t.Fatalf("bundle for an admin: %+v", r.Bundle)
	}

	// The owner still sees exactly what is sent.
	r := apitest.Decode[apitest.ExplainJSON](t, f.Do("POST", "/api/workspaces/sales/diagnosis/explain", `{"preview":true}`, f.Session(apitest.PlanOwner)))
	if !strings.Contains(r.Bundle.Text, "[redacted:DB_PASSWORD]") || strings.Contains(r.Bundle.Text, alicePassword) {
		t.Fatalf("owner's preview: %q", r.Bundle.Text)
	}
}

// When an admin asks, the model is not told the secret's name either, so its
// answer cannot repeat it.
func TestExplainForAnAdminSendsNoSecretNames(t *testing.T) {
	t.Parallel()
	f, p := secretsFixture(t, aitest.Reply{JSON: apitest.RestartAnswer})
	w := f.Do("POST", "/api/workspaces/sales/diagnosis/explain?owner="+apitest.PlanOwner, `{}`, f.Session(rootAdmin, "javo-admins"))
	if w.Code != http.StatusOK || p.Calls() != 1 {
		t.Fatalf("%d %s, %d calls", w.Code, w.Body, p.Calls())
	}
	sent := p.Requests[0].Untrusted
	if strings.Contains(sent, alicePassword) || strings.Contains(sent, "DB_PASSWORD") || !strings.Contains(sent, "[redacted]") {
		t.Fatalf("sent for an admin: %s", sent)
	}
	if body := w.Body.String(); strings.Contains(body, "DB_PASSWORD") || strings.Contains(body, "guess-one") {
		t.Fatalf("answer for an admin: %s", body)
	}
}
