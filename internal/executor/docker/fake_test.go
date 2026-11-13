package docker

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// fake is a client of a daemon played by h, on a unix socket of its own.
func fake(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	// A socket path is at most about 100 bytes: t.TempDir is too deep.
	dir, err := os.MkdirTemp("", "jd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.Listener = l
	srv.Start()
	t.Cleanup(srv.Close)
	return New(sock)
}

// reply writes v as the daemon's JSON answer.
func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// fail answers as the daemon does for an error.
func fail(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"message": msg})
}
