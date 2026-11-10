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
	if !strings.Contains(text, `D="${XDG_CONFIG_HOME:-$HOME/.config}/javo/javo.example.com"`) {
		t.Fatalf("no login folder for this host:\n%s", text)
	}
	if strings.ContainsAny(text, "\u2013\u2014") {
		t.Fatal("an en or em dash in the guide")
	}
}

// TestLLMsTxtNamesRealCalls checks every route llms.txt names exists, and
// every call it runs or names with a method is one a person's read-write
// token may make: not the token routes, plan or device decisions, or the
// trust switch.
func TestLLMsTxtNamesRealCalls(t *testing.T) {
	t.Parallel()
	checkGuideCalls(t, llmsTxt(t), true, func(_ string, o parityOp) bool {
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

// secretFileUse is how a command may touch the files holding the token
// (as an Authorization header) and the device code (as a poll body): pass
// a file to curl, check it, write it or delete it. The secret never
// expands into a command's arguments, which other users may read.
var secretFileUse = regexp.MustCompile(`-H @"\$D/auth"|-d @"\$D/device"|> "\$D/(auth|device)"|\[ -s "\$D/(auth|device)" \]|rm -f "\$D/device"`)

// commandLines is the guide's commands: its indented lines.
func commandLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "    ") {
			out = append(out, line)
		}
	}
	return out
}

// TestLLMsTxtPassesNoSecretInline checks no command puts the token or the
// device code in its arguments: curl reads them from their files.
func TestLLMsTxtPassesNoSecretInline(t *testing.T) {
	t.Parallel()
	for _, line := range commandLines(llmsTxt(t)) {
		rest := secretFileUse.ReplaceAllString(line, "")
		for _, bad := range []string{"$D/auth", "$D/device", "$D/token", "TOKEN", "Bearer $", "$(cat "} {
			if strings.Contains(rest, bad) {
				t.Errorf("the guide passes a secret inline (%s): %s", bad, line)
			}
		}
	}
}

// TestLLMsTxtNeverPrintsTheToken checks no command prints the token: no
// echo or printf names a token, and the poll prints its answer only when it
// holds no token.
func TestLLMsTxtNeverPrintsTheToken(t *testing.T) {
	t.Parallel()
	for _, line := range commandLines(llmsTxt(t)) {
		for _, cmd := range strings.Split(line, ";") {
			cmd = strings.TrimSpace(cmd)
			prints := strings.Contains(cmd, "echo ") || strings.Contains(cmd, "printf ")
			if !prints || strings.Contains(cmd, "|") || strings.Contains(cmd, ">") {
				continue
			}
			if strings.Contains(strings.ToLower(cmd), "token") || (strings.Contains(line, `'"token"'`) && strings.Contains(cmd, "$ANSWER")) {
				t.Errorf("the guide prints the token: %s", line)
			}
		}
	}
}

// TestLLMsTxtStartsAStoppedApp checks the guide starts a stopped app
// rather than restarting it, which leaves it stopped.
func TestLLMsTxtStartsAStoppedApp(t *testing.T) {
	t.Parallel()
	if !strings.Contains(llmsTxt(t), `-X POST -H "X-Javo-Request: 1" -H @"$D/auth" "https://javo.example.com/api/workspaces/NAME/start"`) {
		t.Error("the guide never starts a stopped app")
	}
}

// TestLLMsTxtRunsEachCallOnce checks every call prints its answer and
// status instead of stopping on an error (curl -f), so the agent never runs
// a call twice to read why it failed.
func TestLLMsTxtRunsEachCallOnce(t *testing.T) {
	t.Parallel()
	for _, line := range commandLines(llmsTxt(t)) {
		if strings.Contains(line, "curl -f") || strings.Contains(line, "--fail") {
			t.Errorf("a call stops on an error answer: %s", line)
		}
	}
}
