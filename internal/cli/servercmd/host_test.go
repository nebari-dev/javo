package servercmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// hostConfig writes a config file for an executor that is not Kubernetes,
// with its folders in a folder of the test, and returns the flags that
// name it and an empty SQLite database beside it.
func hostConfig(t *testing.T, executor string) (dir string, args []string) {
	t.Helper()
	dir = t.TempDir()
	yaml := "auth:\n  local:\n    enabled: true\nexecutor: " + executor + "\nkeysDir: " + filepath.Join(dir, "keys") + "\n"
	switch executor {
	case "docker":
		yaml += "docker:\n  gateway: javo\n  socket: " + filepath.Join(dir, "no-daemon.sock") + "\n"
	case "process":
		yaml += "process:\n  stateDir: " + filepath.Join(dir, "state") + "\n  homesDir: " + filepath.Join(dir, "homes") + "\n"
	}
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, []string{"--config", cfg, "--database-path", filepath.Join(dir, "javo.db"), "--runner-image", "example.com/javo:1"}
}

// refused runs cmd with args and returns the error it ended with, which
// the test wants: a command that starts serving fails the test instead.
func refused(t *testing.T, cmd *cobra.Command, args ...string) string {
	t.Helper()
	cmd.SetArgs(args)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	// A command that is not refused would serve until told to stop.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err := cmd.ExecuteContext(ctx)
	if err == nil || ctx.Err() != nil {
		t.Fatalf("%v was not refused: %v", args, err)
	}
	return err.Error()
}

// On Docker, apps reach every port of the container Javo runs in, and as
// processes they share its machine: the only listener is the gateway's
// own, and a metrics or health listener asked for is refused at start
// rather than opened or silently left out. So is every other flag that
// only a cluster gives meaning to.
func TestServerRefusesAClustersFlagsWithAnotherExecutor(t *testing.T) {
	flags := NewServer().Flags()
	for _, name := range append(append([]string{}, clusterFlags...), listenerFlags...) {
		if flags.Lookup(name) == nil {
			t.Errorf("--%s is refused but is no flag of javo server", name)
		}
	}
	for _, executor := range []string{"docker", "process"} {
		_, args := hostConfig(t, executor)
		for flag, want := range map[string]string{
			"--metrics-bind-address=0.0.0.0:9090":    "--metrics-bind-address 0.0.0.0:9090",
			"--health-probe-bind-address=:8082":      "--health-probe-bind-address :8082",
			"--network-policy=false":                 "--network-policy is for executor kubernetes",
			"--sessions-key-secret=javo-sessions":    "--sessions-key-secret is for executor kubernetes",
			"--netprobe-control-url=http://gw:8080/": "--netprobe-control-url is for executor kubernetes",
		} {
			got := refused(t, NewServer(), append(args, "--listen", "127.0.0.1:0", flag)...)
			if !strings.Contains(got, want) || !strings.Contains(got, "executor "+executor) {
				t.Errorf("%s with %s: %q", flag, executor, got)
			}
		}
	}
}

// Without a cluster the gateway and the controller share the one executor
// that runs the apps, so neither runs alone.
func TestOneComponentAloneNeedsACluster(t *testing.T) {
	_, args := hostConfig(t, "process")
	if got := refused(t, NewGateway(), args[:4]...); !strings.Contains(got, "javo gateway needs executor kubernetes") {
		t.Errorf("javo gateway: %q", got)
	}
	got := refused(t, NewServer(), append(args, "--listen", "127.0.0.1:0", "--components", "gateway")...)
	if !strings.Contains(got, "needs executor kubernetes") {
		t.Errorf("--components=gateway: %q", got)
	}
}

// What the host refuses reaches whoever started javo server, in its
// words: a listener beyond loopback for apps as processes, keys that
// others can read, and a Docker daemon that does not answer.
func TestServerDoesNotStartOnAHostItCannotKeepSafe(t *testing.T) {
	_, args := hostConfig(t, "process")
	if got := refused(t, NewServer(), append(args, "--listen", ":8080")...); !strings.Contains(got, "loopback") || !strings.Contains(got, "executor process") {
		t.Errorf("a listener on every address: %q", got)
	}
	dir, args := hostConfig(t, "process")
	if err := os.Mkdir(filepath.Join(dir, "keys"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := refused(t, NewServer(), append(args, "--listen", "127.0.0.1:0")...); !strings.Contains(got, "key folder") || !strings.Contains(got, "0755") {
		t.Errorf("a key folder others can open: %q", got)
	}
	_, args = hostConfig(t, "docker")
	if got := refused(t, NewServer(), append(args, "--listen", "127.0.0.1:0")...); !strings.Contains(got, "docker executor") {
		t.Errorf("no Docker daemon: %q", got)
	}
}
