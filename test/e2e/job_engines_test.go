//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestJobEngines runs one passing input on each engine of each job
// template, in the template's own pixi environment: papermill with the
// Python, R and Julia kernels, marimo and Quarto. A run installs the
// environment into the owner's home on its first start.
func TestJobEngines(t *testing.T) {
	t.Parallel()
	s := newUser(t)
	var me struct{ ID string }
	s.json(http.MethodGet, "/api/me", nil, http.StatusOK, &me)
	c := kube(t)
	ensureHomeClaim(t, c, me.ID)
	ownerUID(t, s, c, me.ID)

	for _, tc := range []struct{ template, engine, kernel, file string }{
		{"jobs-python", "papermill", "python3", "ok-python.ipynb"},
		{"jobs-python", "marimo", "", "ok_marimo.py"},
		{"jobs-python", "quarto", "", "ok.qmd"},
		{"jupyterlab", "papermill", "python3", "ok-python.ipynb"},
		{"jobs-r", "papermill", "ir", "ok-r.ipynb"},
		{"jobs-julia", "papermill", "julia", "ok-julia.ipynb"},
	} {
		t.Run(tc.template+" "+tc.engine, func(t *testing.T) {
			t.Parallel()
			s := s.with(t)
			src, err := os.ReadFile(filepath.Join(repoRoot(t), "test", "e2e", "testdata", "jobs", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			dir := "e2e/engines/" + tc.template + "-" + tc.engine
			runHomeJob(t, s, c, me.ID, "sh", "-c", `mkdir -p "$(dirname "$1")" && printf '%s' "$2" > "$1"`, "seed", "/home/user/"+dir+"/"+tc.file, string(src))
			var job struct{ Name string }
			s.json(http.MethodPost, "/api/jobs", map[string]any{
				"displayName": "e2e " + tc.template + " " + tc.engine + " " + time.Now().Format("150405"),
				"template":    tc.template, "engine": tc.engine, "kernel": tc.kernel, "source": dir + "/" + tc.file,
			}, http.StatusCreated, &job)
			t.Cleanup(func() { s.do(http.MethodDelete, "/api/jobs/"+job.Name, nil) })
			start := time.Now()
			runToEnd(t, s, job.Name)
			t.Logf("%s %s: run succeeded after %s", tc.template, tc.engine, time.Since(start).Round(time.Second))
		})
	}
}
