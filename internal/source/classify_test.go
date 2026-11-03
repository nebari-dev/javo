package source

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nebari-dev/javo/internal/reasons"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gitsync", name+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each row is a real git-sync log tail recorded by scripts/test/record-gitsync-fixtures.sh.
func TestClassifyCloneFixtures(t *testing.T) {
	for fx, want := range map[string]reasons.Code{
		"repo-not-found":   reasons.RepoNotFound,
		"private-no-token": reasons.RepoNotFound,
		"bad-token":        reasons.AuthFailed,
		"ref-not-found":    reasons.RefNotFound,
		"host-unreachable": reasons.CloneFailed,
	} {
		t.Run(fx, func(t *testing.T) {
			got, ok := Classify(CloneContainer, fixture(t, fx))
			if !ok || got != want {
				t.Fatalf("Classify = %q, %v; want %q", got, ok, want)
			}
		})
	}
}

func TestClassifyOtherContainers(t *testing.T) {
	cases := []struct {
		container, message string
		want               reasons.Code
		ok                 bool
	}{
		{CheckContainer, "file-missing: app.py", reasons.FileNotInRepo, true},
		{BuildContainer, "npm ERR! missing script: build", reasons.BuildFailed, true},
		{SiteCopyContainer, "cp: cannot stat '/srv/src/current/dist/.': No such file or directory", reasons.BuildOutputMissing, true},
		{"sample-copy", "cp: cannot stat 'x'", "", false},
		{"app", "anything", "", false},
	}
	for _, c := range cases {
		got, ok := Classify(c.container, c.message)
		if got != c.want || ok != c.ok {
			t.Errorf("Classify(%s, %q) = %q, %v; want %q, %v", c.container, c.message, got, ok, c.want, c.ok)
		}
	}
}

func TestEvidenceIsTheFailingLineNotTheWholeLog(t *testing.T) {
	log := fixture(t, "bad-token") + "{\"msg\":\"debug\",\"detail\":\"password file holds ghp_testtoken123\"}\nplain trailing noise\n"
	ev := strings.Join(Evidence(CloneContainer, log), "\n")
	if !strings.Contains(ev, "Invalid username or token") {
		t.Fatalf("evidence lacks git-sync's own error: %q", ev)
	}
	if strings.Contains(ev, "ghp_testtoken123") || strings.Contains(ev, "--password-file") {
		t.Fatalf("evidence leaks more than the error: %q", ev)
	}
}

func TestEvidenceOfPlainText(t *testing.T) {
	if got := Evidence(CheckContainer, "file-missing: app.py\n"); !slices.Equal(got, []string{"file-missing: app.py"}) {
		t.Fatalf("got %q", got)
	}
}

// A failed build shows its error lines, from the first one on, not only the
// last line: npm's last line only says where its own log went. Blank lines,
// npm's pointer to its log and stack frames are left out. The fixtures are
// the pinned node image's own output.
func TestBuildEvidenceIsItsErrorLines(t *testing.T) {
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join("testdata", "build", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cases := []struct {
		fixture, first string
		has            []string
	}{
		{"npm-ci-no-lockfile", "npm error code EUSAGE", []string{"npm error The `npm ci` command can only install with an existing package-lock.json or"}},
		{"npm-build-script-fails", "Error: ENOENT: no such file or directory, open 'src/index.md'", []string{"Node.js v22.23.4"}},
	}
	for _, c := range cases {
		t.Run(c.fixture, func(t *testing.T) {
			ev := Evidence(BuildContainer, read(c.fixture))
			if len(ev) == 0 || ev[0] != c.first {
				t.Fatalf("evidence starts %q, want %q", ev, c.first)
			}
			if len(ev) > MaxBuildEvidence {
				t.Fatalf("%d lines, want at most %d", len(ev), MaxBuildEvidence)
			}
			for _, h := range c.has {
				if !slices.Contains(ev, h) {
					t.Errorf("evidence lacks %q: %q", h, ev)
				}
			}
			for _, l := range ev {
				if strings.TrimSpace(strings.TrimPrefix(l, "npm error")) == "" || strings.Contains(l, "A complete log of this run") || strings.HasPrefix(strings.TrimSpace(l), "at ") {
					t.Errorf("evidence keeps noise %q", l)
				}
			}
		})
	}
	if got := Evidence(BuildContainer, "step one\nstep two\n\nlast words\n"); !slices.Equal(got, []string{"step one", "step two", "last words"}) {
		t.Fatalf("output without an error line: got %q, want its last lines", got)
	}
}

// A URL (up to 2048 characters) or ref (255) pushes the matching words of
// git-sync's error past the evidence cut; each failure must keep its reason.
func TestClassifyCloneWithLongURLOrRef(t *testing.T) {
	for fx, want := range map[string]reasons.Code{
		"repo-not-found": reasons.RepoNotFound,
		"bad-token":      reasons.AuthFailed,
		"ref-not-found":  reasons.RefNotFound,
	} {
		t.Run(fx, func(t *testing.T) {
			msg := fixture(t, fx)
			if !strings.Contains(msg, "Run(git fetch ") {
				t.Fatalf("fixture has no git fetch command to lengthen: %q", msg)
			}
			msg = strings.Replace(msg, "Run(git fetch ", "Run(git fetch "+strings.Repeat("a", 500), 1)
			if got, ok := Classify(CloneContainer, msg); !ok || got != want {
				t.Fatalf("Classify = %q, %v; want %q", got, ok, want)
			}
		})
	}
}
