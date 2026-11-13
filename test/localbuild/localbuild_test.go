//go:build local

// Package localbuild checks what a javo built with the tag local
// (make local) leaves out, so that it stays small: the Kubernetes client
// and what runs Javo on a cluster, the model providers' clients, the
// secret scanner, the failure-notice engine, the MCP endpoint, the OpenID
// Connect client and the Postgres driver. Each is in files the tag leaves
// out, or in a package only such files import, so none is imported at
// all. That cut does not fail a build when it is undone: an import in a
// file both builds compile only makes the binary larger. The default
// build must still import every one of them, so a name here that has
// moved fails too.
package localbuild

import (
	"os/exec"
	"slices"
	"strings"
	"testing"
)

const root = "../.."

const javo = "github.com/nebari-dev/javo/internal/"

// leftOut is an import path no package of the local build imports, nor
// any path below it, but for the ones named in but.
type leftOut struct {
	path string
	but  []string
}

var notImported = []leftOut{
	// Kubernetes: what is kept is the logger and the signal handler of
	// controller-runtime, client-go's work queue (the loop of a host
	// without a cluster) and the core/v1 types templates and sizes hold.
	{path: "sigs.k8s.io/controller-runtime", but: []string{
		"sigs.k8s.io/controller-runtime/pkg/log",
		"sigs.k8s.io/controller-runtime/pkg/log/zap",
		"sigs.k8s.io/controller-runtime/pkg/manager/signals",
	}},
	{path: "k8s.io/client-go", but: []string{"k8s.io/client-go/util/workqueue"}},
	{path: "k8s.io/api", but: []string{"k8s.io/api/core/v1"}},
	{path: "k8s.io/apiextensions-apiserver"},
	{path: "github.com/google/gnostic-models"},
	{path: "github.com/prometheus"},
	// What runs Javo on a cluster.
	{path: javo + "executor/kube"},
	{path: javo + "controller/namespace"},
	{path: javo + "controller/storage", but: []string{javo + "controller/storage/rows", javo + "controller/storage/onhost"}},
	{path: javo + "controller/preview"},
	{path: javo + "controller/manager"},
	{path: javo + "controller/backupclaim"},
	{path: javo + "controller/secretskey"},
	{path: javo + "controller/loop/managed"},
	{path: javo + "capacity/nodes"},
	{path: javo + "netprobe/kubeprobe"},
	{path: javo + "gateway/clusterview/kubeview"},
	{path: javo + "cli/kubeclient"},
	{path: javo + "storage/inventory"},
	{path: javo + "storage/capability"},
	{path: javo + "storage/choose"},
	// The AI assistant: the providers' clients and the secret scanner.
	{path: "github.com/openai/openai-go/v3"},
	{path: "github.com/anthropics/anthropic-sdk-go"},
	{path: javo + "ai/redact/scanner"},
	{path: "github.com/zricethezav/gitleaks"},
	{path: "github.com/mholt/archives"},
	{path: "github.com/spf13/viper"},
	// Failure notices, the agent endpoint, single sign-on, Postgres.
	{path: javo + "jobs/notify"},
	{path: "github.com/argoproj/notifications-engine"},
	{path: javo + "mcp"},
	{path: "github.com/modelcontextprotocol/go-sdk"},
	{path: javo + "features/authoidc"},
	{path: "github.com/coreos/go-oidc"},
	{path: "github.com/go-jose/go-jose"},
	{path: "github.com/jackc/pgx/v5"},
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
// "/" for the import paths below a path.
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
	for _, out := range notImported {
		kept := func(pkg string) bool { return slices.Contains(out.but, pkg) }
		if len(slices.DeleteFunc(under(whole, out.path, "/"), kept)) == 0 {
			t.Errorf("the default build imports nothing of %s: it has moved, or is no longer used; correct this list", out.path)
		}
		for _, pkg := range out.but {
			if !slices.Contains(local, pkg) {
				t.Errorf("the local build no longer imports %s, which this list lets it keep of %s; correct this list", pkg, out.path)
			}
		}
		if got := slices.DeleteFunc(under(local, out.path, "/"), kept); len(got) > 0 {
			t.Errorf("the local build imports %s (%d of its packages, %s among them), which only files with //go:build !local, "+
				"or packages only they import, may import.\nWhat imports it, first on each line:\n"+
				"  go list -deps -tags local -f '{{.ImportPath}}: {{join .Imports \" \"}}' ./cmd/javo | grep -v '^%s' | grep -F ' %s'",
				out.path, len(got), got[0], out.path, out.path)
		}
	}
}
