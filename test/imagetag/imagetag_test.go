// Package imagetag tests scripts/build/image-tag.sh, which names an image after a
// hash of its build inputs so a deploy builds only images whose inputs
// changed. Each test copies the script into a fresh git repository laid out
// like this one.
package imagetag

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const amd64 = "linux/amd64"

var images = []string{"javo", "fakegithub"}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func write(t *testing.T, repo, path, content string) {
	t.Helper()
	p := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo is a git repository with the script and the build inputs of the
// javo image (with the pod files it carries) and the fake GitHub committed.
func newRepo(t *testing.T) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "build", "image-tag.sh"))
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(t.TempDir(), "javo")
	for path, content := range map[string]string{
		"Dockerfile":                         "FROM scratch\n",
		"go.mod":                             "module x\n",
		"go.sum":                             "",
		"cmd/javo/main.go":                   "package main\n",
		"internal/gateway/gateway.go":        "package gateway\n",
		"ui/src/app.tsx":                     "export {}\n",
		".gitignore":                         "ui/node_modules/\n",
		"docs/readme.md":                     "docs\n",
		"pod/files/javo.bashrc":              "PS1=x\n",
		"pod/files/envs/streamlit/pixi.lock": "version: 6\n",
		"test/e2e/fakegithub/main.go":        "package main\n",
		"deploy/helm/javo/values.yaml":       "a: 1\n",
	} {
		write(t, repo, path, content)
	}
	write(t, repo, "scripts/build/image-tag.sh", string(script))
	run(t, repo, "git", "init", "-q", "-b", "main")
	run(t, repo, "git", "add", ".")
	run(t, repo, "git", "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "init")
	return repo
}

// tags runs the script in repo and returns image name to tag.
func tags(t *testing.T, repo, platform string) map[string]string {
	t.Helper()
	cmd := exec.Command("bash", append([]string{filepath.Join(repo, "scripts", "build", "image-tag.sh"), platform}, images...)...)
	cmd.Dir = t.TempDir() // the repository comes from the script's own path
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("image-tag.sh: %v\n%s", err, stderr)
	}
	got := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || f[1] == "" {
			t.Fatalf("line %q, want \"<name> <tag>\" (all:\n%s)", line, out)
		}
		got[f[0]] = f[1]
	}
	if len(got) != len(images) {
		t.Fatalf("got %v, want a tag for each of %v", got, images)
	}
	return got
}

// changed returns the images whose tag differs between before and after.
func changed(before, after map[string]string) []string {
	var c []string
	for _, n := range images {
		if before[n] != after[n] {
			c = append(c, n)
		}
	}
	return c
}

func TestUnchangedTreeKeepsItsTags(t *testing.T) {
	repo := newRepo(t)
	first := tags(t, repo, amd64)
	if c := changed(first, tags(t, repo, amd64)); len(c) != 0 {
		t.Errorf("tags of %v changed with no change to the tree", c)
	}
	if first["javo"] == first["fakegithub"] {
		t.Errorf("two images with different inputs share tag %s", first["javo"])
	}
}

func TestOnlyImagesWhoseInputsChangedGetANewTag(t *testing.T) {
	for _, tc := range []struct {
		what string
		edit func(t *testing.T, repo string)
		want []string
	}{
		{"a Go file", func(t *testing.T, r string) { write(t, r, "internal/gateway/gateway.go", "package gateway // x\n") }, []string{"javo"}},
		{"a new, uncommitted UI file", func(t *testing.T, r string) { write(t, r, "ui/src/new.tsx", "export {}\n") }, []string{"javo"}},
		{"a deleted Go file", func(t *testing.T, r string) { os.Remove(filepath.Join(r, "cmd/javo/main.go")) }, []string{"javo"}},
		{"a pod file the javo image carries", func(t *testing.T, r string) { write(t, r, "pod/files/javo.bashrc", "PS1=y\n") }, []string{"javo"}},
		{"a bundled environment's lockfile", func(t *testing.T, r string) { write(t, r, "pod/files/envs/streamlit/pixi.lock", "version: 6\n#\n") }, []string{"javo"}},
		{"the fake GitHub", func(t *testing.T, r string) { write(t, r, "test/e2e/fakegithub/main.go", "package main // x\n") }, []string{"fakegithub"}},
		{"docs and the chart", func(t *testing.T, r string) {
			write(t, r, "docs/readme.md", "more docs\n")
			write(t, r, "deploy/helm/javo/values.yaml", "a: 2\n")
		}, nil},
		{"an ignored file", func(t *testing.T, r string) { write(t, r, "ui/node_modules/x/index.js", "x\n") }, nil},
	} {
		t.Run(tc.what, func(t *testing.T) {
			repo := newRepo(t)
			before := tags(t, repo, amd64)
			tc.edit(t, repo)
			if got := changed(before, tags(t, repo, amd64)); strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("changing %s gave new tags to %v, want %v", tc.what, got, tc.want)
			}
		})
	}
}

func TestPlatformIsPartOfTheTag(t *testing.T) {
	repo := newRepo(t)
	if c := changed(tags(t, repo, amd64), tags(t, repo, "linux/arm64")); len(c) != len(images) {
		t.Errorf("only %v differ between platforms, want all of %v", c, images)
	}
}
