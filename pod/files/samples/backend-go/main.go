// A Go back-end: a standard-library web server that shows the server time.
//
// Started as: go run main.go (it listens on $PORT, on every address, or on
// $JAVO_LISTEN_ADDR alone where Javo sets it). The gateway removes the
// app's URL prefix, so the server answers at / and gets the prefix in the
// X-Forwarded-Prefix header; links stay relative.
package main

import (
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := pageTmpl.Execute(w, struct{ Time, Path string }{
			Time: time.Now().UTC().Format("2006-01-02 15:04:05 UTC"),
			Path: strings.TrimSuffix(r.Header.Get("X-Forwarded-Prefix"), "/") + r.URL.Path,
		})
		if err != nil {
			log.Print(err)
		}
	})
	addr := os.Getenv("JAVO_LISTEN_ADDR") + ":" + port
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// html/template escapes the values it fills in.
var pageTmpl = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Your Go app is running</title>
<!-- The page uses IBM Plex Sans and Fira Code when the viewer has them
     installed and system fonts otherwise: no web fonts are fetched, so it
     also works where the app has no internet access. -->
<style>
  :root {
    --bg: #f6f7f9; --card: #ffffff; --text: #16181d; --muted: #5b6270;
    --line: #e3e6eb; --code: #f0f2f5; --link: #2357c6;
    --brand: #00add8; --logo: var(--brand);
    color-scheme: light dark;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0f1115; --card: #171a21; --text: #e8eaee; --muted: #9aa3b2;
      --line: #272c36; --code: #1f232c; --link: #8ab0ff;
      --logo: color-mix(in srgb, var(--brand), #fff 25%);
    }
  }
  * { box-sizing: border-box; }
  body {
    margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 24px 16px;
    background: var(--bg); color: var(--text);
    font: 16px/1.6 "IBM Plex Sans", system-ui, -apple-system, "Segoe UI", Roboto, sans-serif;
  }
  main {
    width: 100%; max-width: 34rem; padding: 40px;
    background: var(--card); border: 1px solid var(--line); border-radius: 16px;
    box-shadow: 0 1px 2px rgb(0 0 0 / 0.04), 0 8px 24px rgb(0 0 0 / 0.04);
  }
  .logo {
    width: 56px; height: 56px; display: grid; place-items: center; border-radius: 14px;
    background: color-mix(in srgb, var(--brand) 12%, transparent);
  }
  .logo svg { width: 32px; height: 32px; fill: var(--logo); }
  h1 { margin: 24px 0 8px; font-size: 1.6rem; font-weight: 600; line-height: 1.25; letter-spacing: -0.01em; }
  p { margin: 0; color: var(--muted); }
  code, dd { font-family: "Fira Code", ui-monospace, SFMono-Regular, Menlo, Consolas, monospace; font-size: 0.875em; }
  code { padding: 0.1em 0.35em; border-radius: 6px; background: var(--code); color: var(--text); overflow-wrap: anywhere; }
  dl { margin: 28px 0 0; padding-top: 20px; border-top: 1px solid var(--line); display: grid; grid-template-columns: auto 1fr; gap: 6px 20px; font-size: 0.875rem; }
  dt { color: var(--muted); }
  dd { margin: 0; overflow-wrap: anywhere; }
  footer { margin-top: 28px; display: flex; align-items: center; gap: 8px; font-size: 0.875rem; color: var(--muted); }
  footer svg { width: 16px; height: 16px; fill: currentColor; }
  a { color: var(--link); text-decoration: none; }
  a:hover { text-decoration: underline; }
  nav { margin-top: 20px; display: flex; flex-wrap: wrap; gap: 8px; }
  nav a { padding: 6px 12px; border: 1px solid var(--line); border-radius: 8px; font-size: 0.875rem; }
  nav a:hover { text-decoration: none; border-color: var(--link); }
  @media (max-width: 480px) { main { padding: 28px 24px; } }
</style>
</head>
<body>
<main>
  <!-- Go logo: Simple Icons 16.35.0 (icons/go.svg), CC0-1.0;
       the logo is the Go project's, see go.dev/brand. -->
  <div class="logo"><svg viewBox="0 0 24 24" role="img" aria-label="Go"><path d="M1.811 10.231c-.047 0-.058-.023-.035-.059l.246-.315c.023-.035.081-.058.128-.058h4.172c.046 0 .058.035.035.07l-.199.303c-.023.036-.082.07-.117.07zM.047 11.306c-.047 0-.059-.023-.035-.058l.245-.316c.023-.035.082-.058.129-.058h5.328c.047 0 .07.035.058.07l-.093.28c-.012.047-.058.07-.105.07zm2.828 1.075c-.047 0-.059-.035-.035-.07l.163-.292c.023-.035.07-.07.117-.07h2.337c.047 0 .07.035.07.082l-.023.28c0 .047-.047.082-.082.082zm12.129-2.36c-.736.187-1.239.327-1.963.514-.176.046-.187.058-.34-.117-.174-.199-.303-.327-.548-.444-.737-.362-1.45-.257-2.115.175-.795.514-1.204 1.274-1.192 2.22.011.935.654 1.706 1.577 1.835.795.105 1.46-.175 1.987-.77.105-.13.198-.27.315-.434H10.47c-.245 0-.304-.152-.222-.35.152-.362.432-.97.596-1.274a.315.315 0 01.292-.187h4.253c-.023.316-.023.631-.07.947a4.983 4.983 0 01-.958 2.29c-.841 1.11-1.94 1.8-3.33 1.986-1.145.152-2.209-.07-3.143-.77-.865-.655-1.356-1.52-1.484-2.595-.152-1.274.222-2.419.993-3.424.83-1.086 1.928-1.776 3.272-2.02 1.098-.2 2.15-.07 3.096.571.62.41 1.063.97 1.356 1.648.07.105.023.164-.117.2m3.868 6.461c-1.064-.024-2.034-.328-2.852-1.029a3.665 3.665 0 01-1.262-2.255c-.21-1.32.152-2.489.947-3.529.853-1.122 1.881-1.706 3.272-1.95 1.192-.21 2.314-.095 3.33.595.923.63 1.496 1.484 1.648 2.605.198 1.578-.257 2.863-1.344 3.962-.771.783-1.718 1.273-2.805 1.495-.315.06-.63.07-.934.106zm2.78-4.72c-.011-.153-.011-.27-.034-.387-.21-1.157-1.274-1.81-2.384-1.554-1.087.245-1.788.935-2.045 2.033-.21.912.234 1.835 1.075 2.21.643.28 1.285.244 1.905-.07.923-.48 1.425-1.228 1.484-2.233z"/></svg></div>
  <h1>Your Go app is running</h1>
  <p>This is a sample app deployed with Javo. Edit <code>samples/backend-go/main.go</code> in your home folder and restart the app to change it.</p>
  <dl>
    <dt>Server time</dt><dd>{{.Time}}</dd>
    <dt>Path</dt><dd>{{.Path}}</dd>
  </dl>
  <footer>
    <svg viewBox="0 0 100 100" aria-hidden="true"><path fill-rule="evenodd" d="M22 0H78A22 22 0 0 1 100 22V78A22 22 0 0 1 78 100H22A22 22 0 0 1 0 78V22A22 22 0 0 1 22 0ZM28.8 80L49.2 20H63.2L42.8 80ZM54.8 80L68.4 40H82.4L68.8 80Z"/></svg>
    <span>Deployed with Javo. <a href="https://javo.nebari.dev">Read the docs</a></span>
  </footer>
</main>
</body>
</html>
`))
