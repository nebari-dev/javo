// Package devenv tests scripts/dev/env.sh, which picks the kind cluster name
// and host ports of a checkout. Each test copies the script into a fresh git
// repository (and its worktrees) under a temporary directory.
package devenv

import (
	"bufio"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// script is scripts/dev/env.sh of this checkout.
func script(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "dev", "env.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

// newRepo is a git repository with scripts/dev/env.sh committed.
func newRepo(t *testing.T) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "javo")
	if err := os.MkdirAll(filepath.Join(repo, "scripts", "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "scripts", "dev", "env.sh"), script(t), 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "git", "init", "-q", "-b", "main")
	run(t, repo, "git", "add", "scripts/dev/env.sh")
	run(t, repo, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "init")
	return repo
}

// worktree adds a linked worktree of repo in a directory called name.
func worktree(t *testing.T, repo, name string) string {
	t.Helper()
	dir := filepath.Join(filepath.Dir(repo), "wt", name)
	run(t, repo, "git", "worktree", "add", "-q", "--detach", dir)
	return dir
}

// resolve runs the script in checkout with env added and returns what it prints.
func resolve(t *testing.T, checkout string, env ...string) map[string]string {
	t.Helper()
	cmd := exec.Command("bash", filepath.Join(checkout, "scripts", "dev", "env.sh"))
	cmd.Dir = t.TempDir() // the checkout comes from the script's own path
	cmd.Env = append(cleanEnv(), env...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("env.sh in %s: %v\n%s", checkout, err, stderr)
	}
	got := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok {
			got[k] = v
		}
	}
	return got
}

// cleanEnv is the test's environment without the variables the script reads.
func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		switch k, _, _ := strings.Cut(kv, "="); k {
		case "JAVO_KIND_NAME", "JAVO_HTTP_PORT", "JAVO_KEYCLOAK_PORT", "JAVO_API_PORT":
		default:
			env = append(env, kv)
		}
	}
	return env
}

func TestMainCheckoutKeepsJavoDev(t *testing.T) {
	got := resolve(t, newRepo(t))
	want := map[string]string{"JAVO_KIND_NAME": "javo-dev", "JAVO_HTTP_PORT": "8080", "JAVO_KEYCLOAK_PORT": "8081"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}

func TestWorktreeGetsItsOwnClusterAndSavedPorts(t *testing.T) {
	wt := worktree(t, newRepo(t), "Feat_X.2")
	first := resolve(t, wt)
	if first["JAVO_KIND_NAME"] != "javo-feat-x-2" {
		t.Errorf("JAVO_KIND_NAME = %q, want javo-feat-x-2", first["JAVO_KIND_NAME"])
	}
	if first["JAVO_HTTP_PORT"] == "8080" || first["JAVO_KEYCLOAK_PORT"] == "8081" {
		t.Errorf("worktree got the main checkout's ports: %v", first)
	}
	if first["JAVO_HTTP_PORT"] == first["JAVO_KEYCLOAK_PORT"] {
		t.Errorf("one port for both: %v", first)
	}
	saved, err := os.ReadFile(filepath.Join(wt, ".dev", "kind.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(saved), "JAVO_HTTP_PORT="+first["JAVO_HTTP_PORT"]+"\n") {
		t.Errorf(".dev/kind.env does not hold the port:\n%s", saved)
	}
	if again := resolve(t, wt); again["JAVO_HTTP_PORT"] != first["JAVO_HTTP_PORT"] || again["JAVO_KEYCLOAK_PORT"] != first["JAVO_KEYCLOAK_PORT"] {
		t.Errorf("second run %v, first %v", again, first)
	}
}

// pick resolves wt, forgets the saved pair and returns the ports it picked.
func pick(t *testing.T, wt string) (http, keycloak string) {
	t.Helper()
	got := resolve(t, wt)
	if err := os.Remove(filepath.Join(wt, ".dev", "kind.env")); err != nil {
		t.Fatal(err)
	}
	return got["JAVO_HTTP_PORT"], got["JAVO_KEYCLOAK_PORT"]
}

func TestPickSkipsAPortInUse(t *testing.T) {
	wt := worktree(t, newRepo(t), "busy")
	http, _ := pick(t, wt)
	l, err := net.Listen("tcp", "127.0.0.1:"+http)
	if err != nil {
		t.Skipf("port %s is taken already: %v", http, err)
	}
	defer l.Close()
	got := resolve(t, wt)
	if got["JAVO_HTTP_PORT"] == http || got["JAVO_KEYCLOAK_PORT"] == http {
		t.Errorf("picked %v although %s is in use", got, http)
	}
}

// A worktree whose cluster is down still owns the ports it saved.
func TestPickSkipsPortsSavedByAnotherWorktree(t *testing.T) {
	repo := newRepo(t)
	b := worktree(t, repo, "b")
	http, keycloak := pick(t, b)
	a := worktree(t, repo, "a")
	resolve(t, a, "JAVO_HTTP_PORT="+http, "JAVO_KEYCLOAK_PORT="+keycloak)
	got := resolve(t, b)
	if got["JAVO_HTTP_PORT"] == http || got["JAVO_KEYCLOAK_PORT"] == keycloak {
		t.Errorf("b picked %v, the ports worktree a saved", got)
	}
}

func TestEnvironmentWinsAndIsSaved(t *testing.T) {
	repo := newRepo(t)
	got := resolve(t, repo, "JAVO_KIND_NAME=javo-other", "JAVO_HTTP_PORT=9100", "JAVO_KEYCLOAK_PORT=9101", "JAVO_API_PORT=9102")
	if got["JAVO_KIND_NAME"] != "javo-other" || got["JAVO_HTTP_PORT"] != "9100" || got["JAVO_KEYCLOAK_PORT"] != "9101" || got["JAVO_API_PORT"] != "9102" {
		t.Errorf("got %v", got)
	}
	if again := resolve(t, repo); again["JAVO_HTTP_PORT"] != "9100" || again["JAVO_KIND_NAME"] != "javo-other" || again["JAVO_API_PORT"] != "9102" {
		t.Errorf("without the environment: %v", again)
	}
}

// Each checkout runs its own Kubernetes API (the Compose install), so
// worktrees get API ports of their own, kept across runs.
func TestWorktreesGetTheirOwnAPIPort(t *testing.T) {
	repo := newRepo(t)
	main := resolve(t, repo)["JAVO_API_PORT"]
	a, b := worktree(t, repo, "a"), worktree(t, repo, "b")
	pa, pb := resolve(t, a)["JAVO_API_PORT"], resolve(t, b)["JAVO_API_PORT"]
	if main == "" || pa == "" || pb == "" {
		t.Fatalf("no API port: main %q, a %q, b %q", main, pa, pb)
	}
	if pa == pb || pa == main || pb == main {
		t.Errorf("API ports collide: main %s, a %s, b %s", main, pa, pb)
	}
	if again := resolve(t, a)["JAVO_API_PORT"]; again != pa {
		t.Errorf("second run %s, first %s", again, pa)
	}
}
