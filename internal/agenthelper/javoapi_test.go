package agenthelper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// javo-api (pod/javo-api), as an agent runs it with only JAVO_API_SOCKET,
// reaches the gateway's /api/me through the helper with the app's token.
func TestJavoAPIReachesTheGatewayThroughTheHelper(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/me" || r.Header.Get("Authorization") != "Bearer app-token-0123456789" {
			http.Error(w, `{"title":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"alice-1"}`))
	}))
	defer gw.Close()
	api, _ := url.Parse(gw.URL)
	dir, err := os.MkdirTemp("", "ah")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "api.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Config{API: api, Token: "app-token-0123456789", Log: quiet()}, sock, "127.0.0.1:0")
	}()
	for deadline := time.Now().Add(5 * time.Second); Ready(sock) != nil; {
		if time.Now().After(deadline) {
			t.Fatal("the helper did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cmd := exec.Command(node, filepath.Join("..", "..", "pod", "javo-api", "javo-api.mjs"), "/api/me")
	cmd.Env = []string{"JAVO_API_SOCKET=" + sock, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), `"id": "alice-1"`) {
		t.Fatalf("javo-api: %v\n%s", err, out)
	}
}
