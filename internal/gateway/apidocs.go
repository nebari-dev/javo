package gateway

import (
	"html/template"
	"net/http"
	"path/filepath"

	"github.com/nebari-dev/javo/internal/gateway/problem"
	"github.com/nebari-dev/javo/internal/model"
	"github.com/nebari-dev/javo/internal/reasons"
)

// APIDocsAssets is the folder of the built UI that holds the Swagger UI
// files (swagger-ui-bundle.js, swagger-ui.css), copied there from the
// pinned swagger-ui-dist package when the UI is built.
const APIDocsAssets = "apidocs"

const apiDocsRoute = "/api/docs/"

// apiDocsCSP allows only this origin: the page, its scripts and styles are
// served here and the document and "Try it out" calls go to this API.
// Swagger UI sets styles through the DOM, which style-src does not govern;
// its icons are data: images.
const apiDocsCSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

var apiDocsPage = template.Must(template.New("docs").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.}} API</title>
<link rel="icon" type="image/svg+xml" href="/favicon.svg">
<link rel="stylesheet" href="swagger-ui.css">
</head><body><div id="swagger-ui"></div>
<script src="swagger-ui-bundle.js"></script>
<script src="init.js"></script>
</body></html>
`))

// apiDocsInit starts Swagger UI on the public document. The validator badge
// would call an outside service, so it is off; authorization is not kept in
// the browser's storage.
const apiDocsInit = `window.addEventListener('load', function () {
  window.ui = SwaggerUIBundle({
    url: '/api/openapi.json',
    dom_id: '#swagger-ui',
    deepLinking: true,
    validatorUrl: null,
    persistAuthorization: false,
  })
})
`

// apiDocs serves the Swagger UI page at /api/docs/ when on, from the assets
// in uiDir; otherwise every docs path is 404. It needs no sign-in: the
// document is public, and calls made from the page carry the token given
// under Authorize, or the session cookie.
func apiDocs(on bool, uiDir string) http.Handler {
	assets := map[string]string{
		"swagger-ui-bundle.js": "text/javascript; charset=utf-8",
		"swagger-ui.css":       "text/css; charset=utf-8",
	}
	return noSniff(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !on || uiDir == "" {
			problem.Write(w, http.StatusNotFound, reasons.Msg(reasons.MsgNoSuchAddress))
			return
		}
		name := r.URL.Path[len(apiDocsRoute):]
		w.Header().Set("Content-Security-Policy", apiDocsCSP)
		switch name {
		case "":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-cache")
			_ = apiDocsPage.Execute(w, model.ProductName)
		case "init.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write([]byte(apiDocsInit))
		default:
			ctype, ok := assets[name]
			if !ok {
				problem.Write(w, http.StatusNotFound, reasons.Msg(reasons.MsgNoSuchAddress))
				return
			}
			w.Header().Set("Content-Type", ctype)
			http.ServeFile(w, r, filepath.Join(uiDir, APIDocsAssets, name))
		}
	}))
}
