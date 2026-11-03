package podfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ansi matches what a prompt draws with but does not show: colours
// (CSI ... m), the window title (OSC ... BEL), readline's markers around
// them and carriage returns.
var ansi = regexp.MustCompile(`\x1b\[[0-9;:]*m|\x1b\][^\a]*\a|[\x01\x02\r]`)

// prompt sources pod/files/javo.bashrc in an interactive bash, runs setup
// (the last command before the prompt), then prints the prompt as bash would
// draw it. It returns the prompt with its escape sequences and without.
func prompt(t *testing.T, env []string, setup string) (raw, text string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	rc, err := filepath.Abs("../../pod/files/javo.bashrc")
	if err != nil {
		t.Fatal(err)
	}
	script := `. "$1"; ` + setup + `
eval "$PROMPT_COMMAND"
printf '%s' "${PS1@P}"`
	cmd := exec.Command("bash", "--noprofile", "--norc", "-i", "-c", script, "bash", rc)
	home := t.TempDir()
	cmd.Env = append([]string{"HOME=" + home, "PATH=" + os.Getenv("PATH"), "TERM=xterm-256color", "LANG=C.UTF-8",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}, env...)
	cmd.Dir = home
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("bash: %v\n%s", err, out)
	}
	return string(out), ansi.ReplaceAllString(string(out), "")
}

// gitRepo makes a repository with one commit on branch, and returns its folder.
func gitRepo(t *testing.T, branch string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "report")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=a", "GIT_AUTHOR_EMAIL=a@example.com", "GIT_COMMITTER_NAME=a", "GIT_COMMITTER_EMAIL=a@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "notes.md")
	run("commit", "-q", "-m", "first")
	return dir
}

// The prompt names the folder and the git branch, marks a change to a
// tracked file, and starts its second line with the status of a command
// that failed.
func TestPromptShowsBranchChangesAndFailedStatus(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t, "feature/plots")
	_, clean := prompt(t, nil, `cd "`+repo+`"; true`)
	if !strings.Contains(clean, "report") || !strings.Contains(clean, "feature/plots") {
		t.Fatalf("prompt %q, want the folder and the branch", clean)
	}
	if strings.Contains(clean, "*") {
		t.Fatalf("prompt %q marks a clean repository as changed", clean)
	}
	last := clean[strings.LastIndex(clean, "\n")+1:]
	if strings.ContainsAny(last, "0123456789") {
		t.Fatalf("second line %q shows a status after a command that succeeded", last)
	}

	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, dirty := prompt(t, nil, `cd "`+repo+`"; (exit 3)`)
	if !strings.Contains(dirty, "feature/plots*") {
		t.Fatalf("prompt %q, want the branch marked as changed", dirty)
	}
	if last := dirty[strings.LastIndex(dirty, "\n")+1:]; !strings.HasPrefix(last, "3 ") {
		t.Fatalf("second line %q, want it to start with the failed status 3", last)
	}
}

// A detached HEAD shows its short commit; a git worktree (whose .git is a
// file) shows its own branch.
func TestPromptReadsDetachedHeadsAndWorktrees(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t, "main")
	_, text := prompt(t, nil, `cd "`+repo+`"; git checkout -q --detach`)
	out, err := exec.Command("git", "-C", repo, "rev-parse", "--short=7", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	if sha := strings.TrimSpace(string(out)); !strings.Contains(text, sha) {
		t.Fatalf("prompt %q, want the detached commit %s", text, sha)
	}
	wt := filepath.Join(t.TempDir(), "other")
	_, text = prompt(t, nil, `git -C "`+repo+`" worktree add -q -b review "`+wt+`" && cd "`+wt+`"`)
	if !strings.Contains(text, "review") {
		t.Fatalf("prompt %q, want the worktree's branch", text)
	}
}

// A branch name is shown, never run: git allows $( and ) in one.
func TestPromptNeverRunsTheBranchName(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t, "x$(echo${IFS}ran)")
	_, text := prompt(t, nil, `cd "`+repo+`"`)
	if !strings.Contains(text, "x$(echo${IFS}ran)") {
		t.Fatalf("prompt %q, want the branch name as it is", text)
	}
}

// The active environment is named: conda's, a venv's, or a pixi shell's.
func TestPromptNamesTheActiveEnvironment(t *testing.T) {
	t.Parallel()
	for _, c := range []struct{ env, want string }{
		{"CONDA_DEFAULT_ENV=/home/user/envs/analysis", "analysis"},
		{"VIRTUAL_ENV=/home/user/project/.venv", ".venv"},
	} {
		if _, text := prompt(t, []string{c.env}, "true"); !strings.Contains(text, c.want) {
			t.Errorf("%s: prompt %q, want %q", c.env, text, c.want)
		}
	}
}

// A dumb terminal (or none) gets a prompt with no escape sequences, and a
// locale that is not UTF-8 one of plain ASCII, so bash measures it right.
func TestPromptDegradesOnDumbTerminals(t *testing.T) {
	t.Parallel()
	repo := gitRepo(t, "main")
	raw, text := prompt(t, []string{"TERM=dumb", "LANG=C"}, `cd "`+repo+`"; false`)
	if strings.Contains(raw, "\x1b") {
		t.Fatalf("prompt %q has escape sequences on a dumb terminal", raw)
	}
	for _, r := range raw {
		if r > 127 {
			t.Fatalf("prompt %q is not ASCII in the C locale", raw)
		}
	}
	if !strings.Contains(text, "main") || !strings.Contains(text, "1 ") {
		t.Fatalf("prompt %q, want the branch and the failed status", text)
	}
}
