import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { test } from 'node:test'
import { run } from './javo-api.mjs'

// A stand-in for the javo API that records what it was asked.
async function server(reply) {
  const seen = []
  const s = createServer((req, res) => {
    let body = ''
    req.on('data', (c) => (body += c))
    req.on('end', () => {
      seen.push({ method: req.method, url: req.url, auth: req.headers.authorization, type: req.headers['content-type'], body })
      const [status, text] = reply(req)
      res.writeHead(status, { 'content-type': 'application/json' }).end(text)
    })
  })
  await new Promise((r) => s.listen(0, '127.0.0.1', r))
  return { url: `http://127.0.0.1:${s.address().port}`, seen, close: () => s.close() }
}

function capture() {
  const out = []
  return { out, write: (s) => out.push(s) }
}

test('GET is the default and sends the token', async () => {
  const s = await server(() => [200, '{"id":"alice-1"}'])
  const o = capture()
  const code = await run(['/api/me'], { JAVO_URL: s.url, JAVO_TOKEN: 'javo_x_y' }, o.write)
  s.close()
  assert.equal(code, 0)
  assert.deepEqual([s.seen[0].method, s.seen[0].url, s.seen[0].auth], ['GET', '/api/me', 'Bearer javo_x_y'])
  assert.match(o.out.join(''), /"id": "alice-1"/)
})

test('a method and a JSON body are sent as JSON', async () => {
  const s = await server(() => [201, '{"name":"demo"}'])
  const code = await run(['POST', '/api/workspaces', '{"template":"custom"}'], { JAVO_URL: s.url, JAVO_TOKEN: 't' }, () => {})
  s.close()
  assert.equal(code, 0)
  assert.equal(s.seen[0].method, 'POST')
  assert.equal(s.seen[0].type, 'application/json')
  assert.deepEqual(JSON.parse(s.seen[0].body), { template: 'custom' })
})

test('an error answer prints the problem and fails', async () => {
  const s = await server(() => [422, '{"title":"Pick a template"}'])
  const o = capture()
  const code = await run(['POST', '/api/workspaces', '{}'], { JAVO_URL: s.url, JAVO_TOKEN: 't' }, o.write)
  s.close()
  assert.equal(code, 1)
  assert.match(o.out.join(''), /422/)
  assert.match(o.out.join(''), /Pick a template/)
})

test('a body that is not JSON is refused before anything is sent', async () => {
  const o = capture()
  const code = await run(['POST', '/api/workspaces', '{nope'], { JAVO_URL: 'http://127.0.0.1:9', JAVO_TOKEN: 't' }, o.write)
  assert.equal(code, 2)
  assert.match(o.out.join(''), /not JSON/)
})

test('without JAVO_URL or JAVO_TOKEN it says what is missing', async () => {
  const o = capture()
  assert.equal(await run(['/api/me'], {}, o.write), 2)
  assert.match(o.out.join(''), /JAVO_URL/)
})

test('the token never appears in what is printed', async () => {
  const s = await server(() => [500, '{"title":"boom"}'])
  const o = capture()
  await run(['/api/me'], { JAVO_URL: s.url, JAVO_TOKEN: 'javo_secret_value' }, o.write)
  s.close()
  assert.doesNotMatch(o.out.join(''), /javo_secret_value/)
})
