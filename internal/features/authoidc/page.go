package authoidc

import (
	"html/template"
	"net/http"

	"github.com/nebari-dev/javo/internal/reasons"
)

var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>body{font:18px/1.5 system-ui,sans-serif;max-width:36em;margin:4em auto;padding:0 1em}
a.btn{display:inline-block;padding:.5em 1em;border:1px solid currentColor;border-radius:.4em;text-decoration:none;color:inherit}</style></head>
<body><h1>{{.Title}}</h1><p>{{.What}}</p><p>{{.Todo}}</p>
{{if .StartAgain}}<p><a class="btn" href="/auth/oidc/start">Start again</a></p>{{end}}
</body></html>
`))

// renderPage answers a sign-in failure with the catalog's words for code,
// never a raw error. Anyone can open this page, so it carries no operator
// detail: that stays in the log and in Provider.Problem.
func renderPage(w http.ResponseWriter, status int, code reasons.Code, startAgain bool) {
	r, _ := reasons.Lookup(code)
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = pageTmpl.Execute(w, map[string]any{
		"Title": r.Title, "What": r.What, "Todo": r.Todo, "StartAgain": startAgain,
	})
}
