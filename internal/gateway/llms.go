package gateway

import (
	"bytes"
	_ "embed"
	"net/http"
	"net/url"
	"text/template"

	"github.com/nebari-dev/javo/internal/gateway/authn"
)

// llmsRoute is the guide a coding agent on a laptop follows to deploy a
// folder to Javo, with only curl and tar.
const llmsRoute = "/llms.txt"

//go:embed llms.txt.tmpl
var llmsText string

var llmsTemplate = template.Must(template.New("llms").Parse(llmsText))

// llmsHandler serves llms.txt with the public URL, where the person's
// browser and the agent reach the gateway, filled in, and its host, which
// names the folder the agent keeps its login in. The public URL is
// publicURL or, when that is empty, the address the request came to
// (authn.PublicURL). It needs no sign-in.
func llmsHandler(publicURL string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := authn.PublicURL(r.Context(), publicURL)
		host := ""
		if u, err := url.Parse(base); err == nil {
			host = u.Host
		}
		var b bytes.Buffer
		if err := llmsTemplate.Execute(&b, struct{ URL, Host string }{base, host}); err != nil {
			panic(err) // the embedded template takes only URL and Host
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(b.Bytes())
	})
}
