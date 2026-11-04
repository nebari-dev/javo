// A Node.js back-end: a plain http server (no packages) that shows the
// server time.
//
// Started as: node server.js (it listens on $PORT). The gateway removes the
// app's URL prefix, so the server answers at / and gets the prefix in the
// X-Forwarded-Prefix header; links stay relative.
const http = require('node:http')

const port = Number(process.env.PORT) || 8080

const escape = (s) =>
  s.replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c])

const server = http.createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost')
  if (req.method !== 'GET' || url.pathname !== '/') {
    res.writeHead(404, { 'Content-Type': 'text/plain' })
    res.end('Not found\n')
    return
  }
  const now = new Date().toISOString().replace('T', ' ').slice(0, 19) + ' UTC'
  const path = (req.headers['x-forwarded-prefix'] || '').replace(/\/$/, '') + url.pathname
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
  res.end(page(now, path))
})

const page = (now, path) => `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Your Node.js app is running</title>
<!-- The page uses IBM Plex Sans and Fira Code when the viewer has them
     installed and system fonts otherwise: no web fonts are fetched, so it
     also works where the app has no internet access. -->
<style>
  :root {
    --bg: #f6f7f9; --card: #ffffff; --text: #16181d; --muted: #5b6270;
    --line: #e3e6eb; --code: #f0f2f5; --link: #2357c6;
    --brand: #5fa04e; --logo: var(--brand);
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
  <!-- Node.js logo: Simple Icons 16.35.0 (icons/nodedotjs.svg), CC0-1.0;
       the logo is the OpenJS Foundation's, see nodejs.org/en/about/branding. -->
  <div class="logo"><svg viewBox="0 0 24 24" role="img" aria-label="Node.js"><path d="M11.998,24c-0.321,0-0.641-0.084-0.922-0.247l-2.936-1.737c-0.438-0.245-0.224-0.332-0.08-0.383 c0.585-0.203,0.703-0.25,1.328-0.604c0.065-0.037,0.151-0.023,0.218,0.017l2.256,1.339c0.082,0.045,0.197,0.045,0.272,0l8.795-5.076 c0.082-0.047,0.134-0.141,0.134-0.238V6.921c0-0.099-0.053-0.192-0.137-0.242l-8.791-5.072c-0.081-0.047-0.189-0.047-0.271,0 L3.075,6.68C2.99,6.729,2.936,6.825,2.936,6.921v10.15c0,0.097,0.054,0.189,0.139,0.235l2.409,1.392 c1.307,0.654,2.108-0.116,2.108-0.89V7.787c0-0.142,0.114-0.253,0.256-0.253h1.115c0.139,0,0.255,0.112,0.255,0.253v10.021 c0,1.745-0.95,2.745-2.604,2.745c-0.508,0-0.909,0-2.026-0.551L2.28,18.675c-0.57-0.329-0.922-0.945-0.922-1.604V6.921 c0-0.659,0.353-1.275,0.922-1.603l8.795-5.082c0.557-0.315,1.296-0.315,1.848,0l8.794,5.082c0.57,0.329,0.924,0.944,0.924,1.603 v10.15c0,0.659-0.354,1.273-0.924,1.604l-8.794,5.078C12.643,23.916,12.324,24,11.998,24z M19.099,13.993 c0-1.9-1.284-2.406-3.987-2.763c-2.731-0.361-3.009-0.548-3.009-1.187c0-0.528,0.235-1.233,2.258-1.233 c1.807,0,2.473,0.389,2.747,1.607c0.024,0.115,0.129,0.199,0.247,0.199h1.141c0.071,0,0.138-0.031,0.186-0.081 c0.048-0.054,0.074-0.123,0.067-0.196c-0.177-2.098-1.571-3.076-4.388-3.076c-2.508,0-4.004,1.058-4.004,2.833 c0,1.925,1.488,2.457,3.895,2.695c2.88,0.282,3.103,0.703,3.103,1.269c0,0.983-0.789,1.402-2.642,1.402 c-2.327,0-2.839-0.584-3.011-1.742c-0.02-0.124-0.126-0.215-0.253-0.215h-1.137c-0.141,0-0.254,0.112-0.254,0.253 c0,1.482,0.806,3.248,4.655,3.248C17.501,17.007,19.099,15.91,19.099,13.993z"/></svg></div>
  <h1>Your Node.js app is running</h1>
  <p>This is a sample app deployed with Javo. Edit <code>samples/backend-node/server.js</code> in your home folder and restart the app to change it.</p>
  <dl>
    <dt>Server time</dt><dd>${escape(now)}</dd>
    <dt>Path</dt><dd>${escape(path)}</dd>
  </dl>
  <footer>
    <svg viewBox="0 0 100 100" aria-hidden="true"><path fill-rule="evenodd" d="M22 0H78A22 22 0 0 1 100 22V78A22 22 0 0 1 78 100H22A22 22 0 0 1 0 78V22A22 22 0 0 1 22 0ZM28.8 80L49.2 20H63.2L42.8 80ZM54.8 80L68.4 40H82.4L68.8 80Z"/></svg>
    <span>Deployed with Javo. <a href="https://javo.nebari.dev">Read the docs</a></span>
  </footer>
</main>
</body>
</html>
`

server.listen(port, () => console.log(`listening on :${port}`))
