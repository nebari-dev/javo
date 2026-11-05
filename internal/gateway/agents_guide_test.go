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

// guideCall is a call to the API that the coding agents' guide names.
var guideCall = regexp.MustCompile(`(javo-api )?(?:(GET|POST|PUT|PATCH|DELETE) )?(/api/[A-Za-z0-9_./{}-]*[A-Za-z0-9_}])`)

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
	ops := apiOperations(t)
	agentWrites := slices.Concat(authn.WorkloadTokenWrites, authn.AgentDeployWrites)
	found := 0
	for _, line := range strings.Split(string(guide), "\n") {
		for _, m := range guideCall.FindAllStringSubmatch(line, -1) {
			viaHelper, method, path := m[1] != "", m[2], m[3]
			if path == "/api/openapi.json" {
				continue // the document itself
			}
			found++
			switch {
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
			if viaHelper && method != "GET" && !slices.Contains(agentWrites, ops[i].id) {
				t.Errorf("the guide runs javo-api %s %s (%s), which a coding agent's token may not call", method, path, ops[i].id)
			}
		}
	}
	if found < 10 {
		t.Fatalf("found only %d API calls in the guide", found)
	}
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
