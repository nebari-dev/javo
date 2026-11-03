package podfiles

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// nebi (pod/bin/nebi) runs the nebi CLI as the app's owner: the token
// is read from the file the kubelet keeps current at every run, so a renewed
// token is used without restarting anything, and it never sits in the
// app's own environment.
func TestTheNebiWrapperReadsTheTokenFileEachRun(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, "nebi")
	if err := os.WriteFile(cli, []byte("#!/bin/sh\necho \"$NEBI_AUTH_TOKEN|$NEBI_REMOTE_URL|$*\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(dir, "token")
	run := func() (string, error) {
		cmd := exec.Command("../../pod/bin/nebi", "workspace", "list")
		cmd.Env = []string{"PATH=/usr/bin:/bin", "JAVO_NEBI_CLI=" + cli, "JAVO_NEBI_TOKEN_FILE=" + tok, "NEBI_REMOTE_URL=http://nebi.example.com"}
		out, err := cmd.CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	for _, want := range []string{"tok-1", "tok-2"} {
		if err := os.WriteFile(tok, []byte(want+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if out, err := run(); err != nil || out != want+"|http://nebi.example.com|workspace list" {
			t.Fatalf("got %q, %v", out, err)
		}
	}
	if err := os.Remove(cli); err != nil {
		t.Fatal(err)
	}
	if out, err := run(); err == nil || !strings.Contains(out, "no Nebi") {
		t.Fatalf("without the CLI: %q, %v", out, err)
	}
}
