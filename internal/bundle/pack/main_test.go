//go:build linux || darwin

package main

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// builtUI is a folder like ui/dist, with what the archive leaves out.
func builtUI(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{
		"index.html": "<html>", "assets/app.js": "js", "assets/app.js.map": "map", "apidocs/swagger-ui-bundle.js": "docs",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// packed is the archive of this repository for platform, by name.
func packed(t *testing.T, src sources) (map[string]*zip.File, []byte) {
	t.Helper()
	src.repo = filepath.Join("..", "..", "..")
	var buf bytes.Buffer
	if _, err := pack(&buf, src); err != nil {
		t.Fatal(err)
	}
	r, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]*zip.File{}
	for _, f := range r.File {
		files[f.Name] = f
	}
	return files, buf.Bytes()
}

func TestTheArchiveHoldsWhatJavoLocalReadsAndNothingElse(t *testing.T) {
	ui := builtUI(t)
	mac, bytesMac := packed(t, sources{ui: ui, platform: "osx-arm64"})
	linux, _ := packed(t, sources{ui: ui, platform: "linux-64"})
	for _, name := range []string{
		"ui/index.html", "ui/assets/app.js",
		"templates/jupyterlab.yaml", "templates/jobs-python.yaml", "templates/terminal.yaml",
		"files/pod-user.sh", "files/envs/jupyterlab/pixi.lock", "files/envs/jobs-python/pixi.lock",
		"files/samples/streamlit/app.py", "files/bin/javo-api", "files/bin/javo-env", "files/agents/terminal.html",
	} {
		if mac[name] == nil {
			t.Errorf("%s is not in the archive", name)
		}
	}
	for _, name := range []string{
		// What javo local never serves or runs.
		"ui/assets/app.js.map", "ui/apidocs/swagger-ui-bundle.js", "files/pod-user.test.mjs",
		// Templates that need an image.
		"templates/container.yaml", "templates/rstudio.yaml", "templates/backup.yaml",
		// Locked for Linux only.
		"templates/pluto.yaml", "files/envs/pluto/pixi.lock", "files/envs/jobs-julia/pixi.lock",
	} {
		if mac[name] != nil {
			t.Errorf("%s is in the archive for a Mac", name)
		}
	}
	for _, name := range []string{"templates/pluto.yaml", "files/envs/pluto/pixi.lock", "files/envs/jobs-julia/pixi.lock"} {
		if linux[name] == nil {
			t.Errorf("%s is not in the archive for Linux", name)
		}
	}
	for name, exec := range map[string]bool{"files/pod-user.sh": true, "files/bin/javo-api": true, "files/javo.bashrc": false} {
		if got := mac[name].Mode()&0o111 != 0; got != exec {
			t.Errorf("%s executable: %v, want %v", name, got, exec)
		}
	}
	for name, f := range mac {
		if !f.Mode().IsRegular() || !fs.ValidPath(name) {
			t.Errorf("%s is not a plain file at a path inside the archive (%v)", name, f.Mode())
		}
	}
	// The same sources, the same bytes: the binary's files are unpacked
	// again only when they changed.
	if _, again := packed(t, sources{ui: ui, platform: "osx-arm64"}); !bytes.Equal(again, bytesMac) {
		t.Error("packing the same sources twice gave different archives")
	}
}

func TestTheTerminalPageIsTheOneGivenOrSaysItIsMissing(t *testing.T) {
	read := func(f *zip.File) string {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var b bytes.Buffer
		_, _ = b.ReadFrom(r)
		return b.String()
	}
	ui := builtUI(t)
	without, _ := packed(t, sources{ui: ui, platform: "linux-64"})
	if page := read(without["files/agents/terminal.html"]); !strings.Contains(page, "built without the terminal page") || !strings.Contains(page, "TERMINAL_PAGE") {
		t.Errorf("the page of a build without one: %q", page)
	}
	page := filepath.Join(t.TempDir(), "terminal.html")
	if err := os.WriteFile(page, []byte("<html>ttyd</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	with, _ := packed(t, sources{ui: ui, platform: "linux-64", terminalPage: page})
	if got := read(with["files/agents/terminal.html"]); got != "<html>ttyd</html>" {
		t.Errorf("the page = %q", got)
	}
}

func TestAUIThatWasNotBuiltIsRefused(t *testing.T) {
	var buf bytes.Buffer
	_, err := pack(&buf, sources{repo: filepath.Join("..", "..", ".."), ui: t.TempDir(), platform: "linux-64"})
	if err == nil || !strings.Contains(err.Error(), "pnpm build") {
		t.Fatalf("pack = %v, want it to say how to build the UI", err)
	}
}
