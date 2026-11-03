// Package shard tests scripts/test/shard.sh, which splits the Go test
// packages into CI shards of about equal time. Each test copies the script
// into a fresh module with a few packages and a timings file.
package shard

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func write(t *testing.T, dir, path, content string) {
	t.Helper()
	p := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newModule is a module example.com/m with a test package for each name in
// tested, a package without tests (notests) and the timings file, whose
// lines are "<import path> <seconds>".
func newModule(t *testing.T, tested []string, timings string) string {
	t.Helper()
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "test", "shard.sh"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	write(t, dir, "go.mod", "module example.com/m\n\ngo 1.24\n")
	write(t, dir, "scripts/test/shard.sh", string(script))
	write(t, dir, "test/timings/sqlite.txt", "# seconds per package\n"+timings)
	write(t, dir, "notests/n.go", "package notests\n")
	for _, name := range tested {
		write(t, dir, name+"/p.go", "package "+name+"\n")
		write(t, dir, name+"/p_test.go", "package "+name+"\n\nimport \"testing\"\n\nfunc TestIn"+name+"(t *testing.T) {}\n")
	}
	return dir
}

func shardCmd(dir string, args ...string) *exec.Cmd {
	cmd := exec.Command("sh", append([]string{filepath.Join(dir, "scripts", "test", "shard.sh")}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "JAVO_TEST_DB=sqlite", "GOFLAGS=-mod=mod", "GOTOOLCHAIN=local")
	return cmd
}

// plan returns the packages of each shard (1..count), from shard.sh plan.
func plan(t *testing.T, dir string, count string) map[string][]string {
	t.Helper()
	out, err := shardCmd(dir, "plan", count, "./...").Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		t.Fatalf("shard.sh plan: %v\n%s", err, stderr)
	}
	got := map[string][]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			t.Fatalf("line %q, want \"<shard> <package>\" (all:\n%s)", line, out)
		}
		got[f[0]] = append(got[f[0]], strings.TrimPrefix(f[1], "example.com/m/"))
	}
	for _, pkgs := range got {
		sort.Strings(pkgs)
	}
	return got
}

func TestEveryTestedPackageIsInExactlyOneShard(t *testing.T) {
	tested := []string{"a", "b", "c", "d", "e", "f", "g"}
	dir := newModule(t, tested, "")
	seen := map[string]int{}
	for _, pkgs := range plan(t, dir, "3") {
		for _, p := range pkgs {
			seen[p]++
		}
	}
	for _, name := range tested {
		if seen[name] != 1 {
			t.Errorf("package %s is in %d shards, want 1", name, seen[name])
		}
	}
	if seen["notests"] != 0 {
		t.Errorf("package notests (no test files) is in %d shards, want 0", seen["notests"])
	}
}

func TestShardsAreCutByPackageTime(t *testing.T) {
	dir := newModule(t, []string{"a", "b", "c"}, "example.com/m/a 30\nexample.com/m/b 15\nexample.com/m/c 15\n")
	got := plan(t, dir, "2")
	want := map[string][]string{"1": {"a"}, "2": {"b", "c"}}
	if strings.Join(got["1"], ",") != strings.Join(want["1"], ",") || strings.Join(got["2"], ",") != strings.Join(want["2"], ",") {
		t.Fatalf("plan %v, want %v", got, want)
	}
}

func TestAPackageMissingFromTheTimingsStillRuns(t *testing.T) {
	dir := newModule(t, []string{"a", "b", "c"}, "example.com/m/a 30\n")
	got := plan(t, dir, "2")
	// a alone outweighs the two new packages together.
	if strings.Join(got["1"], ",") != "a" || strings.Join(got["2"], ",") != "b,c" {
		t.Fatalf("plan %v, want shard 1 [a], shard 2 [b c]", got)
	}
}

func TestAShardRunsOnlyItsPackagesAndReportsTheirTimes(t *testing.T) {
	dir := newModule(t, []string{"a", "b"}, "example.com/m/a 30\nexample.com/m/b 1\n")
	out, err := shardCmd(dir, "2", "2", "./...").CombinedOutput()
	if err != nil {
		t.Fatalf("shard.sh 2 2: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "ok  \texample.com/m/b") || strings.Contains(s, "example.com/m/a\t") {
		t.Fatalf("shard 2 should run b only:\n%s", s)
	}
	times := s[strings.Index(s, "::group::test times"):]
	if !strings.Contains(times, "example.com/m/b ") {
		t.Fatalf("no time for example.com/m/b in the test times group:\n%s", s)
	}
}

func TestAFailingTestFailsTheShard(t *testing.T) {
	dir := newModule(t, []string{"a"}, "")
	write(t, dir, "a/fail_test.go", "package a\n\nimport \"testing\"\n\nfunc TestFails(t *testing.T) { t.Fatal(\"boom\") }\n")
	out, err := shardCmd(dir, "1", "1", "./...").CombinedOutput()
	if err == nil {
		t.Fatalf("shard.sh passed with a failing test:\n%s", out)
	}
	if !strings.Contains(string(out), "boom") {
		t.Fatalf("the failure is not shown:\n%s", out)
	}
}
