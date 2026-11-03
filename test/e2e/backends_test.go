//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestBackends runs the Node and Go back-end templates on a file of the home
// with a typed command, in their environments (node and go from
// conda-forge), and reads the page through the gateway.
func TestBackends(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)
	ensureHomeClaim(t, c, me.ID)
	ownerUID(t, s, c, me.ID)

	for _, tc := range []struct{ template, file, src, command string }{
		{"backend-node", "server.js", `require("http").createServer((q, r) => r.end("hello from node")).listen(process.env.PORT)`, "node server.js"},
		{"backend-go", "main.go", "package main\n\nimport (\n\t\"fmt\"\n\t\"net/http\"\n\t\"os\"\n)\n\nfunc main() {\n\thttp.HandleFunc(\"/\", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, \"hello from go\") })\n\t_ = http.ListenAndServe(\":\"+os.Getenv(\"PORT\"), nil)\n}\n", "go run main.go"},
	} {
		t.Run(tc.template, func(t *testing.T) {
			t.Parallel()
			s := s.with(t)
			dir := "e2e/backends/" + tc.template
			runHomeJob(t, s, c, me.ID, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf '%s' "$2" > "$1"`, "seed", "/home/user/"+dir+"/"+tc.file, tc.src)
			a := s.createApp(map[string]any{"displayName": "e2e " + tc.template + " " + time.Now().Format("150405"), "template": tc.template, "file": dir + "/" + tc.file, "command": tc.command})
			t.Cleanup(func() { s.do(http.MethodDelete, "/api/workspaces/"+a.Name, nil) })
			got := s.waitFor(a.Name, 8*time.Minute, func(a app) bool {
				if a.Phase == "Failed" {
					t.Fatalf("%s failed: %+v", tc.template, a.Steps)
				}
				return a.Phase == "Running"
			})
			resp, body := s.do(http.MethodGet, got.URL, nil)
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "hello from") {
				t.Fatalf("GET %s: %d %.300s", got.URL, resp.StatusCode, body)
			}
		})
	}
}
