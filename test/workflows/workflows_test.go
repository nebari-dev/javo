// Package workflows checks the rules every GitHub Actions workflow in
// .github/workflows must follow, so a new workflow or step cannot quietly
// widen what a workflow run can do:
//   - every action is pinned to a full commit SHA (a tag can be moved);
//   - the workflow-wide token is read-only: write scopes go on the job that
//     needs them;
//   - only main writes caches, so no branch, pull request or tag can plant
//     a cache that main or a release then restores;
//   - repository and organisation secrets (anything but GITHUB_TOKEN) and
//     OIDC tokens reach only jobs bound to an environment, whose deployment
//     rules say which refs may run them (release: tags v*);
//   - actions/checkout does not leave the token in .git/config.
package workflows

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type step struct {
	Name string         `json:"name"`
	Uses string         `json:"uses"`
	If   any            `json:"if"`
	With map[string]any `json:"with"`
}

type job struct {
	Permissions any    `json:"permissions"`
	Environment any    `json:"environment"`
	Uses        string `json:"uses"`
	Steps       []step `json:"steps"`
}

type workflow struct {
	Permissions any                        `json:"permissions"`
	Jobs        map[string]json.RawMessage `json:"jobs"`
}

var (
	pinned     = regexp.MustCompile(`^[^@\s]+@[0-9a-f]{40}$`)
	pinnedDock = regexp.MustCompile(`^docker://[^@\s]+@sha256:[0-9a-f]{64}$`)
	expr       = regexp.MustCompile(`\$\{\{(.*?)\}\}`)
	secretRef  = regexp.MustCompile(`\bsecrets\b(\s*\.\s*[A-Za-z0-9_]+)?`)
	mainOnly   = regexp.MustCompile(`github\.ref\s*==\s*'refs/heads/main'`)
)

// usesOK says whether a uses: value names exactly one immutable thing.
func usesOK(uses string) bool {
	return strings.HasPrefix(uses, "./") || pinned.MatchString(uses) || pinnedDock.MatchString(uses)
}

// readOnly says whether a permissions value grants no write scope.
func readOnly(p any) bool {
	switch v := p.(type) {
	case string:
		return v == "read-all" || v == "{}"
	case map[string]any:
		for _, level := range v {
			if level != "read" && level != "none" {
				return false
			}
		}
		return true
	}
	return false
}

// grants says whether a permissions value gives scope write.
func grants(p any, scope string) bool {
	switch v := p.(type) {
	case string:
		return v == "write-all"
	case map[string]any:
		return v[scope] == "write"
	}
	return false
}

func environmentName(e any) string {
	switch v := e.(type) {
	case string:
		return v
	case map[string]any:
		s, _ := v["name"].(string)
		return s
	}
	return ""
}

// secretsUsed lists the secrets a job's expressions read, other than the
// run's own GITHUB_TOKEN; "secrets" alone means the whole set (toJSON).
func secretsUsed(raw []byte) []string {
	seen := map[string]bool{}
	for _, m := range expr.FindAllSubmatch(raw, -1) {
		for _, s := range secretRef.FindAllSubmatch(m[1], -1) {
			name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(s[1])), "."))
			if name == "GITHUB_TOKEN" {
				continue
			}
			if name == "" {
				name = "secrets"
			}
			seen[name] = true
		}
	}
	var out []string
	for n := range seen {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// problems lists every rule a workflow file breaks.
func problems(file string, src []byte) []string {
	var out []string
	add := func(format string, args ...any) {
		out = append(out, file+": "+fmt.Sprintf(format, args...))
	}
	var w workflow
	if err := yaml.Unmarshal(src, &w); err != nil {
		return []string{file + ": " + err.Error()}
	}
	if w.Permissions == nil {
		add("no top-level permissions (the repository default applies)")
	} else if !readOnly(w.Permissions) {
		add("top-level permissions grant a write scope; give it to the job that needs it")
	}
	ids := make([]string, 0, len(w.Jobs))
	for id := range w.Jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		raw := w.Jobs[id]
		var j job
		if err := json.Unmarshal(raw, &j); err != nil {
			add("job %s: %v", id, err)
			continue
		}
		env := environmentName(j.Environment)
		if j.Uses != "" && !usesOK(j.Uses) {
			add("job %s: %q is not pinned to a commit SHA", id, j.Uses)
		}
		if s := secretsUsed(raw); len(s) > 0 && env != "release" {
			add("job %s: reads %s outside the release environment", id, strings.Join(s, ", "))
		}
		if grants(j.Permissions, "id-token") && env == "" {
			add("job %s: gets an OIDC token (id-token: write) without an environment", id)
		}
		for i, s := range j.Steps {
			where := fmt.Sprintf("job %s step %d (%s)", id, i+1, s.Name+s.Uses)
			if s.Uses == "" {
				continue
			}
			if !usesOK(s.Uses) {
				add("%s: %q is not pinned to a commit SHA", where, s.Uses)
			}
			action, _, _ := strings.Cut(s.Uses, "@")
			cond, _ := s.If.(string)
			switch action {
			case "actions/cache":
				add("%s: actions/cache saves from every ref; use actions/cache/restore and a main-only actions/cache/save", where)
			case "actions/cache/save":
				if !mainOnly.MatchString(cond) {
					add("%s: saves a cache outside main (needs if: github.ref == 'refs/heads/main')", where)
				}
			case "actions/checkout":
				if v, ok := s.With["persist-credentials"]; !ok || v != false {
					add("%s: actions/checkout without persist-credentials: false", where)
				}
			}
		}
	}
	return out
}

func TestEveryWorkflowFollowsTheRules(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".github", "workflows", "*.y*ml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no workflows found: %v", err)
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range problems(filepath.Base(f), src) {
			t.Error(p)
		}
	}
}

// Each rule, on a workflow that breaks it, so the check above cannot pass by
// checking nothing.
func TestTheRulesCatchWhatTheyAreFor(t *testing.T) {
	const sha = "0123456789abcdef0123456789abcdef01234567"
	cases := map[string]struct {
		src  string
		want string
	}{
		"tag instead of SHA": {
			"permissions: {contents: read}\njobs:\n  a:\n    steps:\n      - uses: actions/setup-go@v7\n",
			`"actions/setup-go@v7" is not pinned`,
		},
		"short SHA": {
			"permissions: {contents: read}\njobs:\n  a:\n    steps:\n      - uses: actions/setup-go@b7ad1da\n",
			"is not pinned",
		},
		"reusable workflow by branch": {
			"permissions: {contents: read}\njobs:\n  a:\n    uses: org/repo/.github/workflows/x.yml@main\n",
			"is not pinned",
		},
		"no top-level permissions": {
			"jobs:\n  a:\n    steps: []\n",
			"no top-level permissions",
		},
		"top-level write": {
			"permissions: {contents: read, packages: write}\njobs:\n  a:\n    steps: []\n",
			"top-level permissions grant a write scope",
		},
		"write-all": {
			"permissions: write-all\njobs:\n  a:\n    steps: []\n",
			"top-level permissions grant a write scope",
		},
		"combined cache": {
			"permissions: {contents: read}\njobs:\n  a:\n    steps:\n      - uses: actions/cache@" + sha + "\n",
			"actions/cache saves from every ref",
		},
		"cache saved from any ref": {
			"permissions: {contents: read}\njobs:\n  a:\n    steps:\n      - uses: actions/cache/save@" + sha + "\n",
			"saves a cache outside main",
		},
		"cache saved from pull requests too": {
			"permissions: {contents: read}\njobs:\n  a:\n    steps:\n      - uses: actions/cache/save@" + sha + "\n        if: github.ref != 'refs/heads/main'\n",
			"saves a cache outside main",
		},
		"secret outside an environment": {
			"permissions: {contents: read}\njobs:\n  a:\n    steps:\n      - run: echo\n        env:\n          T: ${{ secrets.QUAY_TOKEN }}\n",
			"reads QUAY_TOKEN outside the release environment",
		},
		"secret in another environment": {
			"permissions: {contents: read}\njobs:\n  a:\n    environment: staging\n    steps:\n      - uses: docker/login-action@" + sha + "\n        with:\n          password: ${{ secrets.QUAY_TOKEN }}\n",
			"reads QUAY_TOKEN outside the release environment",
		},
		"every secret at once": {
			"permissions: {contents: read}\njobs:\n  a:\n    steps:\n      - run: echo\n        env:\n          ALL: ${{ toJSON(secrets) }}\n",
			"reads secrets outside the release environment",
		},
		"OIDC token without an environment": {
			"permissions: {contents: read}\njobs:\n  a:\n    permissions: {id-token: write}\n    steps: []\n",
			"without an environment",
		},
		"checkout keeps the token": {
			"permissions: {contents: read}\njobs:\n  a:\n    steps:\n      - uses: actions/checkout@" + sha + "\n",
			"without persist-credentials: false",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := strings.Join(problems("x.yml", []byte(c.src)), "\n")
			if !strings.Contains(got, c.want) {
				t.Fatalf("want a problem containing %q, got:\n%s", c.want, got)
			}
		})
	}

	ok := "permissions: {contents: read}\njobs:\n" +
		"  build:\n    steps:\n" +
		"      - uses: actions/checkout@" + sha + "\n        with:\n          persist-credentials: false\n" +
		"      - uses: actions/cache/restore@" + sha + "\n" +
		"      - uses: actions/cache/save@" + sha + "\n        if: github.ref == 'refs/heads/main' && steps.c.outputs.cache-hit != 'true'\n" +
		"      - run: echo\n        env:\n          GH_TOKEN: ${{ secrets.GITHUB_TOKEN }}\n" +
		"  release:\n    environment: {name: release}\n    permissions: {id-token: write, packages: write}\n    steps:\n" +
		"      - uses: docker://alpine@sha256:" + strings.Repeat("a", 64) + "\n" +
		"      - run: echo\n        env:\n          T: ${{ secrets.QUAY_TOKEN }}\n"
	if p := problems("ok.yml", []byte(ok)); len(p) > 0 {
		t.Fatalf("a workflow that follows every rule is refused:\n%s", strings.Join(p, "\n"))
	}
}
