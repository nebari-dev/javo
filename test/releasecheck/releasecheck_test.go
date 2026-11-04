// Package releasecheck tests scripts/release/check-release-commit.sh, which
// the release workflow runs before it builds anything: the tagged commit
// must be on main, and the CI workflows must have passed on main for that
// exact commit. GitHub's API is a fake gh on the PATH that answers from
// files.
package releasecheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const sha = "0123456789abcdef0123456789abcdef01234567"

// fakeGH answers `gh api <path>` with <dir>/<name>.json, where name is
// "compare" for the compare call and the workflow file for a runs call,
// and logs each path it was asked for to <dir>/calls.
const fakeGH = `#!/bin/sh
[ "$1" = api ] || { echo "unexpected gh $*" >&2; exit 2; }
echo "$2" >> "$FAKE/calls"
case "$2" in
  */compare/*) f=compare ;;
  */actions/workflows/*/runs\?*) f=${2#*/actions/workflows/}; f=${f%%/runs*} ;;
  *) echo "unexpected path $2" >&2; exit 2 ;;
esac
[ -f "$FAKE/$f.json" ] || { echo "gh: Not Found (HTTP 404)" >&2; exit 1; }
cat "$FAKE/$f.json"
`

func run(t *testing.T, answers map[string]string, args ...string) (string, string, error) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(fakeGH), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range answers {
		if err := os.WriteFile(filepath.Join(dir, name+".json"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "release", "check-release-commit.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(script, args...)
	cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "FAKE="+dir, "GITHUB_REPOSITORY=org/javo")
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
	return string(out), string(calls), err
}

func runs(status, conclusion string) string {
	return `{"total_count":1,"workflow_runs":[{"status":"` + status + `","conclusion":"` + conclusion + `","html_url":"https://github.example/run/1"}]}`
}

func passing() map[string]string {
	return map[string]string{
		"compare": `{"status":"identical"}`,
		"go.yml":  runs("completed", "success"),
		"ui.yml":  runs("completed", "success"),
	}
}

func TestACommitOnMainThatPassedCIIsReleased(t *testing.T) {
	for _, status := range []string{"identical", "ahead"} {
		a := passing()
		a["compare"] = `{"status":"` + status + `"}`
		out, calls, err := run(t, a, sha, "go.yml", "ui.yml")
		if err != nil {
			t.Fatalf("compare %s: refused: %v\n%s", status, err, out)
		}
		for _, want := range []string{
			"repos/org/javo/compare/" + sha + "...main",
			"repos/org/javo/actions/workflows/go.yml/runs?head_sha=" + sha + "&branch=main&event=push",
			"repos/org/javo/actions/workflows/ui.yml/runs?head_sha=" + sha + "&branch=main&event=push",
		} {
			if !strings.Contains(calls, want) {
				t.Errorf("never asked for %s; calls:\n%s", want, calls)
			}
		}
	}
}

func TestACommitNotOnMainIsRefused(t *testing.T) {
	// "behind": the commit has changes main does not (a branch, or a tag
	// on an unmerged commit); "diverged": both have changes.
	for _, status := range []string{"behind", "diverged"} {
		a := passing()
		a["compare"] = `{"status":"` + status + `"}`
		out, _, err := run(t, a, sha, "go.yml", "ui.yml")
		if err == nil || !strings.Contains(out, "is not on main") {
			t.Fatalf("compare %s: want a refusal naming main, got err=%v\n%s", status, err, out)
		}
	}
}

func TestACommitGitHubDoesNotKnowIsRefused(t *testing.T) {
	a := passing()
	delete(a, "compare")
	if out, _, err := run(t, a, sha, "go.yml", "ui.yml"); err == nil {
		t.Fatalf("released a commit the compare call could not find:\n%s", out)
	}
}

func TestACommitWhoseCIDidNotPassIsRefused(t *testing.T) {
	cases := map[string]struct {
		runs string
		want string
	}{
		"failed":      {runs("completed", "failure"), "did not pass"},
		"cancelled":   {runs("completed", "cancelled"), "did not pass"},
		"skipped":     {runs("completed", "skipped"), "did not pass"},
		"still going": {runs("in_progress", ""), "has not finished"},
		"queued":      {runs("queued", ""), "has not finished"},
		"never ran":   {`{"total_count":0,"workflow_runs":[]}`, "no ui.yml run"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a := passing()
			a["ui.yml"] = c.runs
			out, _, err := run(t, a, sha, "go.yml", "ui.yml")
			if err == nil || !strings.Contains(out, c.want) {
				t.Fatalf("want a refusal containing %q, got err=%v\n%s", c.want, err, out)
			}
		})
	}
}

func TestAnythingButAFullCommitSHAIsRefused(t *testing.T) {
	for _, bad := range []string{"", "main", "0123456", sha + "0", strings.ToUpper(sha), "../" + sha[3:]} {
		out, calls, err := run(t, passing(), bad, "go.yml")
		if err == nil {
			t.Errorf("%q: accepted\n%s", bad, out)
		}
		if calls != "" {
			t.Errorf("%q: called the API before checking the commit:\n%s", bad, calls)
		}
	}
}
