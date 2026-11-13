//go:build local

// Package localbuild checks what a javo built with the tag local
// (make local) leaves out, so that it stays small: the model providers'
// clients, the failure-notice engine and the Postgres driver are in files
// the tag leaves out, and the cluster's client set, informers and cache
// are dropped by the linker, since everything that reaches them asks
// buildinfo.Local first. Neither cut fails a build when it is undone: an
// import in a file both builds compile, or a cluster call placed ahead of
// that question, only makes the binary larger. The default build must
// still import every one of them, so a name here that has moved fails too.
package localbuild

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../.."

// notImported are the modules no package of the local build imports.
var notImported = []string{
	"github.com/openai/openai-go/v3",
	"github.com/anthropics/anthropic-sdk-go",
	"github.com/argoproj/notifications-engine",
	"github.com/jackc/pgx/v5",
}

// notLinked are packages the local build still imports (the admin
// commands keep a cluster client) and whose functions the linker must
// find no use of. A name ending in /... is the packages under it.
var notLinked = []string{
	"k8s.io/client-go/kubernetes",
	"k8s.io/client-go/kubernetes/typed/...",
	"k8s.io/client-go/informers/...",
	"k8s.io/client-go/tools/leaderelection/resourcelock",
	"sigs.k8s.io/controller-runtime/pkg/cache",
	"sigs.k8s.io/controller-runtime/pkg/leaderelection",
}

func goCmd(t *testing.T, args ...string) []string {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, stderr)
	}
	return strings.Split(strings.TrimSpace(string(out)), "\n")
}

// under lists the names that are prefix, or prefix and then one of seps:
// "/" for the import paths below a path, "." for the functions of a
// package, whose symbols are its path, a dot and their name.
func under(names []string, prefix, seps string) []string {
	var out []string
	for _, name := range names {
		rest, ok := strings.CutPrefix(name, prefix)
		if ok && (rest == "" || strings.ContainsRune(seps, rune(rest[0]))) {
			out = append(out, name)
		}
	}
	return out
}

func TestALocalBuildImportsNoneOfWhatItsFilesLeaveOut(t *testing.T) {
	local := goCmd(t, "list", "-deps", "-tags", "local", "./cmd/javo")
	whole := goCmd(t, "list", "-deps", "./cmd/javo")
	for _, module := range notImported {
		if len(under(whole, module, "/")) == 0 {
			t.Errorf("the default build imports nothing of %s: it has moved, or is no longer used; correct this list", module)
		}
		if got := under(local, module, "/"); len(got) > 0 {
			t.Errorf("the local build imports %s (%d of its packages), which only files with //go:build !local may import.\n"+
				"What imports it, first on each line:\n"+
				"  go list -deps -tags local -f '{{.ImportPath}}: {{join .Imports \" \"}}' ./cmd/javo | grep -v '^%s' | grep -F ' %s'",
				module, len(got), module, module)
		}
	}
}

func TestALocalBuildLinksNoneOfWhatOnlyAClusterUses(t *testing.T) {
	whole := goCmd(t, "list", "-deps", "./cmd/javo")
	bin := filepath.Join(t.TempDir(), "javo")
	// -w: no debug information, which nothing here reads and takes the
	// longest to write.
	goCmd(t, "build", "-tags", "local", "-ldflags=-w", "-o", bin, "./cmd/javo")
	var funcs []string
	for _, line := range goCmd(t, "tool", "nm", bin) {
		if _, name, ok := strings.Cut(line, " T "); ok {
			funcs = append(funcs, name)
		} else if _, name, ok := strings.Cut(line, " t "); ok {
			funcs = append(funcs, name)
		}
	}
	if len(under(funcs, "main", ".")) == 0 {
		t.Fatalf("go tool nm lists no function of main among %d", len(funcs))
	}
	for _, name := range notLinked {
		pkg, below, seps := name, "", "."
		if tree, ok := strings.CutSuffix(name, "/..."); ok {
			pkg, below, seps = tree, "/", "./"
		}
		if len(under(whole, pkg, below)) == 0 {
			t.Errorf("the default build imports nothing of %s: it has moved, or is no longer used; correct this list", name)
		}
		if got := under(funcs, pkg, seps); len(got) > 0 {
			t.Errorf("the local build links %s (%d functions, %s among them): something reaches it without asking buildinfo.Local first, "+
				"or asks after it has made a cluster call.\nWhat refers to it, one line a reference:\n"+
				"  go build -tags local -ldflags=-dumpdep -o /dev/null ./cmd/javo 2>&1 | grep -F -- '-> %s'",
				name, len(got), got[0], pkg)
		}
	}
}
