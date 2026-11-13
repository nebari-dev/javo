//go:build linux || darwin

// Command pack writes the archive the binary of `javo local` carries
// (internal/bundle) from this repository and a built UI:
//
//	go run ./internal/bundle/pack -ui ui/dist -os darwin -arch arm64 -o internal/bundle/assets/bundle.zip
//
// Run from the repository's root. It leaves out what `javo local` never
// reads, and prints the size of what it kept and what it left out.
package main

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nebari-dev/javo/internal/bundle"
	"github.com/nebari-dev/javo/internal/controller/workspace"
	"github.com/nebari-dev/javo/internal/executor/process"
	"github.com/nebari-dev/javo/internal/gateway"
	"github.com/nebari-dev/javo/internal/templates"
)

// sources is where the archive's files come from.
type sources struct {
	// repo is the repository's root and ui the built UI (ui/dist).
	repo, ui string
	// terminalPage is the page the terminal and the coding agents serve,
	// built as the Dockerfile's pod-files stage builds it; empty puts
	// noTerminalPage in its place.
	terminalPage string
	// platform is what pixi calls the machine the binary is for.
	platform string
}

// noTerminalPage stands in for the terminal page (agents/terminal.html)
// in a binary built without one: the page is generated from the ttyd
// binary and an npm package (pod/terminal/page.mjs), which a build
// without them cannot do. The terminal and the coding agents then start,
// and whoever opens one reads why there is no terminal in the browser.
const noTerminalPage = `<!doctype html><meta charset="utf-8"><title>No terminal page</title>
<p>This Javo was built without the terminal page, so the terminal cannot be shown here.
Build it again with <code>make local TERMINAL_PAGE=&lt;terminal.html&gt;</code>; the configuration page of the documentation says how the page is made.</p>
`

// file is one file of the archive.
type file struct {
	name string
	data []byte
	exec bool
}

// part is the size of one folder of the archive, kept or left out.
type part struct {
	what             string
	files, raw, size int
}

func main() {
	var src sources
	var goos, goarch, out string
	flag.StringVar(&src.ui, "ui", "ui/dist", "the built UI")
	flag.StringVar(&src.terminalPage, "terminal-page", "", "the built terminal page (agents/terminal.html); empty: a page that says it is missing")
	flag.StringVar(&goos, "os", "", "GOOS of the binary")
	flag.StringVar(&goarch, "arch", "", "GOARCH of the binary")
	flag.StringVar(&out, "o", "", "the archive to write")
	flag.Parse()
	src.repo = "."
	if src.platform = process.Platform(goos, goarch); src.platform == "" || out == "" {
		fmt.Fprintf(os.Stderr, "pack: -o is required, and -os %q -arch %q is no machine javo local runs on\n", goos, goarch)
		os.Exit(2)
	}
	var buf bytes.Buffer
	parts, err := pack(&buf, src)
	if err == nil {
		err = os.WriteFile(out, buf.Bytes(), 0o644)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
		os.Exit(1)
	}
	for _, p := range parts {
		fmt.Printf("%-44s %4d files %9d bytes %9d compressed\n", p.what, p.files, p.raw, p.size)
	}
	fmt.Printf("%s: %d bytes for %s\n", out, buf.Len(), src.platform)
}

// pack writes the archive to w and returns the size of each part, kept
// and left out. The same sources give the same bytes: the files are
// sorted and carry no date.
func pack(w io.Writer, src sources) ([]part, error) {
	kept, left, err := gather(src)
	if err != nil {
		return nil, err
	}
	zw := zip.NewWriter(w)
	zw.RegisterCompressor(zip.Deflate, func(out io.Writer) (io.WriteCloser, error) {
		return flate.NewWriter(out, flate.BestCompression)
	})
	var parts []part
	for _, group := range kept {
		p, err := write(zw, group)
		if err != nil {
			return nil, err
		}
		parts = append(parts, p)
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	for _, group := range left {
		p := sizeOf(group)
		p.what = "left out: " + p.what
		parts = append(parts, p)
	}
	return parts, nil
}

// group is the files of one part of the archive.
type group struct {
	what  string
	files []file
}

// write adds g's files to the archive, sorted and without a date.
func write(zw *zip.Writer, g group) (part, error) {
	slices.SortFunc(g.files, func(a, b file) int { return strings.Compare(a.name, b.name) })
	for _, f := range g.files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		h.SetMode(0o644)
		if f.exec {
			h.SetMode(0o755)
		}
		out, err := zw.CreateHeader(h)
		if err != nil {
			return part{}, err
		}
		if _, err := out.Write(f.data); err != nil {
			return part{}, err
		}
	}
	return sizeOf(g), nil
}

// sizeOf is how many bytes g's files are, and how many compressed as the
// archive compresses them.
func sizeOf(g group) part {
	p := part{what: g.what, files: len(g.files)}
	for _, f := range g.files {
		p.raw += len(f.data)
		p.size += compressed(f.data)
	}
	return p
}

// compressed is the size data has in the archive, without its header.
func compressed(data []byte) int {
	var n counter
	fw, _ := flate.NewWriter(&n, flate.BestCompression)
	_, _ = fw.Write(data)
	_ = fw.Close()
	return int(n)
}

type counter int

func (c *counter) Write(p []byte) (int, error) { *c += counter(len(p)); return len(p), nil }

// gather reads the sources into the parts the archive keeps and the ones
// it leaves out:
//   - of the UI, the API docs page's Swagger UI (the page is off unless a
//     config file turns it on, and `javo local` has none) and source maps;
//   - the templates nothing of which runs without containers on the
//     platform (workspace.RunsWithoutContainers), and the bundled
//     environments only they use;
//   - the tests of the platform's files.
func gather(src sources) (kept, left []group, err error) {
	ui, docs, err := read(src.ui, bundle.UIDir, func(name string) bool {
		return strings.HasPrefix(name, gateway.APIDocsAssets+"/") || path.Ext(name) == ".map"
	})
	if err != nil {
		return nil, nil, fmt.Errorf("the built UI: %w", err)
	}
	if !slices.ContainsFunc(ui, func(f file) bool { return f.name == bundle.UIDir+"/index.html" }) {
		return nil, nil, fmt.Errorf("the built UI: %s has no index.html (build it: cd ui && pnpm install --frozen-lockfile && pnpm build)", src.ui)
	}

	envs := map[string]bool{}
	all, _, err := read(filepath.Join(src.repo, "deploy/helm/javo/files/templates"), bundle.TemplatesDir, nil)
	if err != nil {
		return nil, nil, err
	}
	var tpls, noTpls []file
	for _, f := range all {
		t, err := templates.Parse(f.data)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", f.name, err)
		}
		if workspace.RunsWithoutContainers(&t, src.platform) != nil {
			noTpls = append(noTpls, f)
			continue
		}
		tpls = append(tpls, f)
		envs[t.Spec.Environment] = true
	}

	files, noFiles, err := read(filepath.Join(src.repo, "pod/files"), bundle.FilesDir, func(name string) bool {
		if env, ok := strings.CutPrefix(name, "envs/"); ok {
			env, _, _ = strings.Cut(env, "/")
			return !envs[env]
		}
		return strings.HasSuffix(name, ".test.mjs")
	})
	if err != nil {
		return nil, nil, err
	}
	// What the image adds to pod/files: its bin folder and the terminal page.
	bin, _, err := read(filepath.Join(src.repo, "pod/bin"), bundle.FilesDir+"/bin", nil)
	if err != nil {
		return nil, nil, err
	}
	api, err := os.ReadFile(filepath.Join(src.repo, "pod/javo-api/javo-api.mjs"))
	if err != nil {
		return nil, nil, err
	}
	bin = append(bin, file{name: bundle.FilesDir + "/bin/javo-api", data: api})
	for i := range bin {
		bin[i].exec = true
	}
	page := []byte(noTerminalPage)
	if src.terminalPage != "" {
		if page, err = os.ReadFile(src.terminalPage); err != nil {
			return nil, nil, fmt.Errorf("the terminal page: %w", err)
		}
	}
	terminal := []file{{name: bundle.FilesDir + "/agents/terminal.html", data: page}}

	kept = []group{{"the web UI", ui}, {"the templates", tpls}, {"the platform's files", files}, {"their bin folder", bin}, {"the terminal page", terminal}}
	left = []group{
		{"the API docs page and source maps of the UI", docs},
		{"templates that need containers or another system", noTpls},
		{"their environments, and the files' tests", noFiles},
	}
	return kept, left, nil
}

// read is the files of the folder dir, named under prefix: the ones skip
// says nothing about, and the ones it names. A symbolic link is refused:
// the archive holds plain files only.
func read(dir, prefix string, skip func(name string) bool) (kept, skipped []file, err error) {
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s is not a plain file", p)
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		f := file{name: prefix + "/" + filepath.ToSlash(rel), data: data, exec: info.Mode()&0o111 != 0}
		if skip != nil && skip(filepath.ToSlash(rel)) {
			skipped = append(skipped, f)
		} else {
			kept = append(kept, f)
		}
		return nil
	})
	return kept, skipped, err
}
