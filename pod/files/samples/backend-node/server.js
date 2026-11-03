// A Node.js back-end: a plain http server (no packages) that shows the
// server time.
//
// Started as: node server.js (it listens on $PORT). The gateway removes the
// app's URL prefix, so the server answers at /; links stay relative.
const http = require('node:http')

const port = Number(process.env.PORT) || 8080

const server = http.createServer((req, res) => {
  if (req.method !== 'GET' || new URL(req.url, 'http://localhost').pathname !== '/') {
    res.writeHead(404, { 'Content-Type': 'text/plain' })
    res.end('Not found\n')
    return
  }
  const now = new Date().toISOString().replace('T', ' ').slice(0, 19) + ' UTC'
  res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8' })
  res.end(
    '<!doctype html><meta charset=utf-8><title>Hello from Node.js</title>' +
      `<h1>Hello from Node.js</h1><p>The server time is ${now}.</p>` +
      '<p>Edit server.js and restart the app to change this page.</p>',
  )
})

server.listen(port, () => console.log(`listening on :${port}`))
