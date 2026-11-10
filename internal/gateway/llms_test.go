package gateway_test

import (
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/apitest"
	"github.com/nebari-dev/javo/internal/gateway/authn"
)

// llmsTxt is /llms.txt as a gateway at https://javo.example.com serves it
// to anyone, signed in or not.
func llmsTxt(t *testing.T) string {
	t.Helper()
	f := apitest.New(t, apitest.Options{PublicURL: "https://javo.example.com"})
	w := f.Do("GET", "/llms.txt", "", nil)
	wantCode(t, "llms.txt", w, http.StatusOK)
	if ct := w.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Fatalf("Cache-Control %q", cc)
	}
	return w.Body.String()
}

// commandPlaceholder is what an app's command may hold for Javo to fill:
// the only {{ the guide may show.
var commandPlaceholder = regexp.MustCompile(`\{\{\.(Port|BaseURL|Home)\}\}`)

func TestLLMsTxtServedWithPublicURL(t *testing.T) {
	t.Parallel()
	text := llmsTxt(t)
	if !strings.Contains(text, "https://javo.example.com/api/auth/device") {
		t.Fatalf("no device login at the public address:\n%s", text)
	}
	if strings.Contains(commandPlaceholder.ReplaceAllString(text, ""), "{{") {
		t.Fatalf("an unfilled template action:\n%s", text)
	}
	if strings.ContainsAny(text, "\u2013\u2014") {
		t.Fatal("an en or em dash in the guide")
	}
}

// TestLLMsTxtNamesRealCalls checks every route llms.txt names exists, and
// every call it runs is one a person's read-write token may make: not the
// token routes, plan or device decisions, or the trust switch.
func TestLLMsTxtNamesRealCalls(t *testing.T) {
	t.Parallel()
	checkGuideCalls(t, llmsTxt(t), func(_ string, o parityOp) bool {
		return !o.sessionOnly() && !authn.IsTrustChange(o.path)
	})
}

// TestLLMsTxtNamesNoFramework checks the guide puts no shipped template
// forward by name: the agent picks one from /api/templates. Only custom and
// container, which serve any code or image, may be named as a value.
func TestLLMsTxtNamesNoFramework(t *testing.T) {
	t.Parallel()
	text := llmsTxt(t)
	files, err := filepath.Glob(filepath.Join("..", "..", "deploy", "helm", "javo", "files", "templates", "*.yaml"))
	if err != nil || len(files) < 5 {
		t.Fatalf("templates: %v %v", files, err)
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".yaml")
		if name == "custom" || name == "container" {
			continue
		}
		for _, v := range []string{`"template":"` + name + `"`, "`" + name + "`"} {
			if strings.Contains(text, v) {
				t.Errorf("the guide names the template %s", v)
			}
		}
	}
	if !strings.Contains(text, `"template":"custom"`) {
		t.Error("the guide does not show the custom template")
	}
}
