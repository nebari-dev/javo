package backupcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nebari-dev/javo/internal/backup"
	"github.com/nebari-dev/javo/internal/cli/clitest"
)

// fakeAPI answers the full backup endpoints: the run starts with every
// step waiting, and each GET moves the steps along until they end.
type fakeAPI struct {
	// unavailable answers this many GETs with 503 first, as a gateway that
	// restarts does.
	unavailable int
	hosts       []string
	mu          sync.Mutex
	started     []string
	gets        int
	final       string // the last progress, JSON
	steps       []string
}

func (f *fakeAPI) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no token", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		f.hosts = append(f.hosts, r.Host)
		switch {
		case r.Method == http.MethodPost && (r.URL.Path == "/api/admin/backups/runs" || r.URL.Path == "/api/admin/backups/restores"):
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			for k, v := range body {
				if v == "" {
					t.Errorf("sent an empty %s: the API refuses an empty mode", k)
				}
			}
			f.started = append(f.started, r.URL.Path+" "+body["run"]+" "+body["from"]+" "+body["person"]+" "+body["mode"])
			_, _ = w.Write([]byte(f.steps[0]))
		case r.Method == http.MethodGet && f.unavailable > 0:
			f.unavailable--
			http.Error(w, "restarting", http.StatusServiceUnavailable)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/run-1") && r.URL.Query().Get("logs") == "true":
			f.gets++
			_, _ = w.Write([]byte(f.steps[min(f.gets, len(f.steps)-1)]))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	})
}

const (
	waiting = `{"run":"run-1","done":false,"failed":0,"steps":[
	  {"kind":"database","name":"database","state":"Waiting","status":{"title":"Waiting"},"evidence":[],"log":[]},
	  {"kind":"home","name":"alice-1234abcd","person":"Alice (alice, local)","account":true,"state":"Waiting","status":{"title":"Waiting"},"evidence":[],"log":[]}]}`
	halfway = `{"run":"run-1","done":false,"failed":0,"steps":[
	  {"kind":"database","name":"javo-1.db","state":"Done","status":{"title":"Copied to the bucket"},"evidence":[],"log":[]},
	  {"kind":"home","name":"alice-1234abcd","person":"Alice (alice, local)","account":true,"state":"Running","status":{"title":"Backing up"},"evidence":[],"log":[]}]}`
	ended = `{"run":"run-1","done":true,"failed":1,"steps":[
	  {"kind":"database","name":"javo-1.db","state":"Done","status":{"title":"Copied to the bucket"},"evidence":[],"log":[]},
	  {"kind":"home","name":"alice-1234abcd","person":"Alice (alice, local)","account":true,"state":"Failed","status":{"title":"Backup bucket refused the key"},"evidence":["Access Denied"],"log":["restic: Access Denied"]}]}`
)

func runFollow(t *testing.T, f *fakeAPI, args ...string) (string, error) {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	defer srv.Close()
	t.Setenv("JAVO_URL", srv.URL)
	if os.Getenv("JAVO_INTERNAL") == "1" {
		t.Setenv("JAVO_URL", "https://javo.example.com")
		t.Setenv("JAVO_API_URL", srv.URL)
	}
	t.Setenv("JAVO_TOKEN", "tok")
	t.Setenv("JAVO_RUN_ID", "run-1")
	var out bytes.Buffer
	root := clitest.Root(New(nil))
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"backup"}, append(args, "--poll", "1ms")...))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := root.ExecuteContext(ctx)
	return out.String(), err
}

// `javo backup run` is what a run of the backup job runs: it starts the full
// backup for its own run, prints each step as it changes with the step's
// own log once it ended, and fails the run when a step failed.
func TestBackupRunFollowsTheFullBackupAndFailsOnAFailedStep(t *testing.T) {
	f := &fakeAPI{steps: []string{waiting, halfway, ended}}
	out, err := runFollow(t, f, "run")
	if len(f.started) != 1 || !strings.HasPrefix(f.started[0], "/api/admin/backups/runs run-1") {
		t.Fatalf("started = %v", f.started)
	}
	for _, want := range []string{
		`step=database name=javo-1.db action=done status="Copied to the bucket"`,
		`step=home name=alice-1234abcd person="Alice (alice, local)" action=running status="Backing up"`,
		`step=home name=alice-1234abcd person="Alice (alice, local)" action=failed status="Backup bucket refused the key" evidence="Access Denied"`,
		"restic: Access Denied", "SUMMARY", "1 of 2 parts failed: the database, 1 home folder of a person, 0 home volumes with no account and 0 shared folders"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if err == nil {
		t.Fatal("a run with a failed step succeeded")
	}
}

// `javo backup restore-run` takes what to restore from the job's parameters.
func TestRestoreRunPassesItsParameters(t *testing.T) {
	ok := strings.ReplaceAll(strings.ReplaceAll(ended, `"failed":1`, `"failed":0`), `"state":"Failed"`, `"state":"Done"`)
	// The gateway restarting meanwhile does not end the run.
	f := &fakeAPI{steps: []string{waiting, ok}, unavailable: 2}
	t.Setenv("JAVO_PARAM_FROM", "backup-7")
	t.Setenv("JAVO_PARAM_PERSON", "alice-1234abcd")
	t.Setenv("JAVO_PARAM_MODE", "beside")
	// Inside the cluster: the gateway's own address, the public host.
	t.Setenv("JAVO_INTERNAL", "1")
	out, err := runFollow(t, f, "restore-run")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if len(f.started) != 1 || f.started[0] != "/api/admin/backups/restores run-1 backup-7 alice-1234abcd beside" {
		t.Fatalf("started = %v", f.started)
	}
	for _, h := range f.hosts {
		if h != "javo.example.com" {
			t.Fatalf("called with Host %q, want the public host", h)
		}
	}
	if !strings.Contains(out, "All 2 parts done: the database, 1 home folder of a person, 0 home volumes with no account and 0 shared folders") {
		t.Errorf("output:\n%s", out)
	}
}

// `javo backup job owner` says who owns a folder, for the backup of a home
// volume without an account.
func TestBackupJobOwnerReportsWhoOwnsTheFolder(t *testing.T) {
	dir, result := t.TempDir(), filepath.Join(t.TempDir(), "result")
	root := clitest.Root(New(nil))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"backup", "job", "owner", "--dir", dir, "--result-file", result})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	b, err := os.ReadFile(result)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := backup.ParseJobResult(string(b))
	if !ok || got.UID != int64(os.Getuid()) || got.GID != int64(os.Getgid()) {
		t.Fatalf("result %s: %+v", b, got)
	}
	if !strings.Contains(out.String(), fmt.Sprintf("action=owner dir=%s uid=%d", dir, os.Getuid())) {
		t.Errorf("log: %s", out.String())
	}
}
