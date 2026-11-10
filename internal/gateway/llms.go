package gateway

import (
	"bytes"
	_ "embed"
	"net/http"
	"net/url"
	"strings"
	"text/template"
)

// llmsRoute is the guide a coding agent on a laptop follows to deploy a
// folder to Javo, with only curl and tar.
const llmsRoute = "/llms.txt"

//go:embed llms.txt.tmpl
var llmsText string

var llmsTemplate = template.Must(template.New("llms").Parse(llmsText))

// llmsHandler serves llms.txt with publicURL, where the person's browser
// and the agent reach the gateway, filled in, and its host, which names the
// folder the agent keeps its login in. It needs no sign-in.
func llmsHandler(publicURL string) http.Handler {
	var b bytes.Buffer
	host := ""
	if u, err := url.Parse(publicURL); err == nil {
		host = u.Host
	}
	if err := llmsTemplate.Execute(&b, struct{ URL, Host string }{strings.TrimSuffix(publicURL, "/"), host}); err != nil {
		panic(err) // the embedded template takes only URL and Host
	}
	page := b.Bytes()
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(page)
	})
}
