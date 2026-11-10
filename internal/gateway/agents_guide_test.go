package gateway_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/gateway/authn"
)

// guideCall is a call to the API that a guide for coding agents names.
var guideCall = regexp.MustCompile(`(javo-api )?(?:(GET|POST|PUT|PATCH|DELETE) )?(/api/[A-Za-z0-9_./{}-]*[A-Za-z0-9_}])`)

// curlMethod is the -X of a curl command.
var curlMethod = regexp.MustCompile(`\s-X\s+([A-Z]+)`)

// TestAgentsGuideNamesRealCalls reads the API guide that coding-agent apps
// get (internal/controller/workspace/agents_guide.md): every route it names
// is a route of the API document with that method, and every write it runs
// with javo-api is one a coding agent's own token may call. A renamed route,
// or a guide that tells the agent to call one its token is refused, fails
// here.
func TestAgentsGuideNamesRealCalls(t *testing.T) {
	t.Parallel()
	guide, err := os.ReadFile(filepath.Join("..", "controller", "workspace", "agents_guide.md"))
	if err != nil {
		t.Fatal(err)
	}
	agentWrites := slices.Concat(authn.WorkloadTokenWrites, authn.AgentDeployWrites)
	checkGuideCalls(t, string(guide), false, func(method string, o parityOp) bool {
		return method == "GET" || slices.Contains(agentWrites, o.id)
	})
}

// checkGuideCalls checks every API route guide names: it is a route of the
// API document with that method, and each call the guide runs (a javo-api
// or curl line, and with prose also any METHOD /api/... it names) is one
// allowed lets the agent's token make.
func checkGuideCalls(t *testing.T, guide string, prose bool, allowed func(method string, o parityOp) bool) {
	t.Helper()
	ops := apiOperations(t)
	found := 0
	// A command continued on the next line is one line.
	for _, line := range strings.Split(strings.ReplaceAll(guide, "\\\n", " "), "\n") {
		curl := runsCurl(line)
		for _, m := range guideCall.FindAllStringSubmatch(line, -1) {
			viaHelper, method, path := m[1] != "", m[2], m[3]
			if path == "/api/openapi.json" {
				continue // the document itself
			}
			found++
			runs := viaHelper || curl != "" || (prose && method != "")
			switch {
			case curl != "":
				method = curl
			case method == "" && viaHelper:
				method = "GET" // javo-api's default
			case method == "":
				// A route named in prose without a method: it must exist.
				if !slices.ContainsFunc(ops, func(o parityOp) bool { return guidePathMatches(o.path, path) }) {
					t.Errorf("the guide names %s, which is not in the API document", path)
				}
				continue
			}
			i := slices.IndexFunc(ops, func(o parityOp) bool { return o.method == method && guidePathMatches(o.path, path) })
			if i < 0 {
				t.Errorf("the guide names %s %s, which is not in the API document", method, path)
				continue
			}
			if runs && !allowed(method, ops[i]) {
				t.Errorf("the guide runs %s %s (%s), which the agent's token may not call", method, path, ops[i].id)
			}
		}
	}
	if found < 10 {
		t.Fatalf("found only %d API calls in the guide", found)
	}
}

// runsCurl is the method of the curl command on line, or "" when the line
// runs none: -X names it, -T uploads with PUT, -d posts, and GET otherwise.
func runsCurl(line string) string {
	i := strings.Index(line, "curl ")
	if i < 0 {
		return ""
	}
	cmd := " " + line[i:]
	switch m := curlMethod.FindStringSubmatch(cmd); {
	case m != nil:
		return m[1]
	case strings.Contains(cmd, " -T "):
		return "PUT"
	case strings.Contains(cmd, " -d "):
		return "POST"
	}
	return "GET"
}

// guidePathMatches reports whether p, a path as the guide writes it (with
// placeholders such as NAME and maybe a query), is the document's
// route pattern.
func guidePathMatches(pattern, p string) bool {
	p, _, _ = strings.Cut(p, "?")
	ps, gs := strings.Split(pattern, "/"), strings.Split(p, "/")
	if len(ps) != len(gs) {
		return false
	}
	for i := range ps {
		if ps[i] != gs[i] && !strings.HasPrefix(ps[i], "{") {
			return false
		}
	}
	return true
}
