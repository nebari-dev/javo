//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os/exec"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// javoCLI runs `javo args...` in the pod that holds the database: deploy/javo
// with SQLite, deploy/javo-controller with Postgres (the same choice
// scripts/install/ensure-local-user.sh makes), with that pod's database flags appended.
// It returns stdout; stderr goes into the failure message only, so a
// password written to stdout (--password-file -) is never logged.
func javoCLI(t *testing.T, args ...string) string {
	t.Helper()
	target, container, db := "deploy/javo", "javo", []string{"--database-engine=sqlite", "--database-path=/var/lib/javo/javo.db"}
	if !sqliteInstall(t) {
		target, container, db = "deploy/javo-controller", "controller", []string{"--database-engine=postgres", "--database-dsn-file=/etc/javo-db/dsn"}
	}
	argv := append([]string{"-n", gatewayNS, "exec", target, "-c", container, "--", "/javo"}, args...)
	cmd := exec.Command("kubectl", append(argv, db...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		t.Fatalf("javo %v: %v\n%s", args, err, errb.String())
	}
	return out.String()
}

// sqliteInstall reports whether the release runs SQLite: one Deployment
// called javo runs both components.
func sqliteInstall(t *testing.T) bool {
	t.Helper()
	var d appsv1.Deployment
	err := kube(t).Get(context.Background(), client.ObjectKey{Namespace: gatewayNS, Name: "javo"}, &d)
	if err == nil {
		return true
	}
	if client.IgnoreNotFound(err) != nil {
		t.Fatal(err)
	}
	return false
}
