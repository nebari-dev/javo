"""A custom app: a standard-library web server that shows the server time.

Started as: python sample.py --port <port> --base-url <prefix>
"""
import argparse
import html
from datetime import datetime, timezone
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from string import Template

parser = argparse.ArgumentParser()
parser.add_argument("--port", type=int, required=True)
parser.add_argument("--base-url", required=True)
args = parser.parse_args()
base = args.base_url if args.base_url.endswith("/") else args.base_url + "/"


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.split("?")[0] != base:
            self.send_error(404)
            return
        now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M:%S UTC")
        path = html.escape(self.path.split("?")[0])
        body = PAGE.substitute(time=html.escape(now), path=path).encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


PAGE = Template("""<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Your custom app is running</title>
<!-- The page uses IBM Plex Sans and Fira Code when the viewer has them
     installed and system fonts otherwise: no web fonts are fetched, so it
     also works where the app has no internet access. -->
<style>
  :root {
    --bg: #f6f7f9; --card: #ffffff; --text: #16181d; --muted: #5b6270;
    --line: #e3e6eb; --code: #f0f2f5; --link: #2357c6;
    --brand: #3776ab; --logo: var(--brand);
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
  <!-- Python logo: Simple Icons 16.35.0 (icons/python.svg), CC0-1.0;
       the logo is the Python Software Foundation's, see python.org/community/logos. -->
  <div class="logo"><svg viewBox="0 0 24 24" role="img" aria-label="Python"><path d="M14.25.18l.9.2.73.26.59.3.45.32.34.34.25.34.16.33.1.3.04.26.02.2-.01.13V8.5l-.05.63-.13.55-.21.46-.26.38-.3.31-.33.25-.35.19-.35.14-.33.1-.3.07-.26.04-.21.02H8.77l-.69.05-.59.14-.5.22-.41.27-.33.32-.27.35-.2.36-.15.37-.1.35-.07.32-.04.27-.02.21v3.06H3.17l-.21-.03-.28-.07-.32-.12-.35-.18-.36-.26-.36-.36-.35-.46-.32-.59-.28-.73-.21-.88-.14-1.05-.05-1.23.06-1.22.16-1.04.24-.87.32-.71.36-.57.4-.44.42-.33.42-.24.4-.16.36-.1.32-.05.24-.01h.16l.06.01h8.16v-.83H6.18l-.01-2.75-.02-.37.05-.34.11-.31.17-.28.25-.26.31-.23.38-.2.44-.18.51-.15.58-.12.64-.1.71-.06.77-.04.84-.02 1.27.05zm-6.3 1.98l-.23.33-.08.41.08.41.23.34.33.22.41.09.41-.09.33-.22.23-.34.08-.41-.08-.41-.23-.33-.33-.22-.41-.09-.41.09zm13.09 3.95l.28.06.32.12.35.18.36.27.36.35.35.47.32.59.28.73.21.88.14 1.04.05 1.23-.06 1.23-.16 1.04-.24.86-.32.71-.36.57-.4.45-.42.33-.42.24-.4.16-.36.09-.32.05-.24.02-.16-.01h-8.22v.82h5.84l.01 2.76.02.36-.05.34-.11.31-.17.29-.25.25-.31.24-.38.2-.44.17-.51.15-.58.13-.64.09-.71.07-.77.04-.84.01-1.27-.04-1.07-.14-.9-.2-.73-.25-.59-.3-.45-.33-.34-.34-.25-.34-.16-.33-.1-.3-.04-.25-.02-.2.01-.13v-5.34l.05-.64.13-.54.21-.46.26-.38.3-.32.33-.24.35-.2.35-.14.33-.1.3-.06.26-.04.21-.02.13-.01h5.84l.69-.05.59-.14.5-.21.41-.28.33-.32.27-.35.2-.36.15-.36.1-.35.07-.32.04-.28.02-.21V6.07h2.09l.14.01zm-6.47 14.25l-.23.33-.08.41.08.41.23.33.33.23.41.08.41-.08.33-.23.23-.33.08-.41-.08-.41-.23-.33-.33-.23-.41-.08-.41.08z"/></svg></div>
  <h1>Your custom app is running</h1>
  <p>This is a sample app deployed with Javo. Edit <code>samples/custom/sample.py</code> in your home folder and restart the app to change it.</p>
  <dl>
    <dt>Server time</dt><dd>$time</dd>
    <dt>Path</dt><dd>$path</dd>
  </dl>
  <footer>
    <svg viewBox="0 0 100 100" aria-hidden="true"><path fill-rule="evenodd" d="M22 0H78A22 22 0 0 1 100 22V78A22 22 0 0 1 78 100H22A22 22 0 0 1 0 78V22A22 22 0 0 1 22 0ZM28.8 80L49.2 20H63.2L42.8 80ZM54.8 80L68.4 40H82.4L68.8 80Z"/></svg>
    <span>Deployed with Javo. <a href="https://javo.nebari.dev">Read the docs</a></span>
  </footer>
</main>
</body>
</html>
""")

ThreadingHTTPServer(("0.0.0.0", args.port), Handler).serve_forever()
