package gateway

import (
	"net/http"
	"path"
	"strings"
)

const uiCSP = "default-src 'self'; style-src 'self'; font-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// uiHandler serves the built UI from dir. Paths that are not a file get
// index.html so client-side routes work; /api/ and /u/ never fall back.
// imgOrigins are the hosts of branding images given as a URL; images load
// from nowhere else.
func uiHandler(dir string, imgOrigins []string) http.Handler {
	csp := uiCSP
	if len(imgOrigins) > 0 {
		csp = "img-src 'self' " + strings.Join(imgOrigins, " ") + "; " + uiCSP
	}
	root := http.Dir(dir)
	files := http.FileServer(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := path.Clean("/" + r.URL.Path)
		if p == "/api" || p == "/u" || strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/u/") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if p != "/" && p != "/index.html" && isFile(root, p) {
			files.ServeHTTP(w, r)
			return
		}
		serveIndex(w, r, root)
	})
}

func isFile(root http.FileSystem, name string) bool {
	f, err := root.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	return err == nil && fi.Mode().IsRegular()
}

func serveIndex(w http.ResponseWriter, r *http.Request, root http.FileSystem) {
	f, err := root.Open("/index.html")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(w, r, "index.html", fi.ModTime(), f)
}
