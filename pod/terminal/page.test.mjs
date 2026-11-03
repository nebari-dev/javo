// node --test page.test.mjs (the image build runs it before building the page).
import assert from 'node:assert/strict'
import { test } from 'node:test'
import { buildPage } from './page.mjs'

const ttyd = '<!DOCTYPE html><html><head><title>ttyd</title></head><body><script>ttyd()</script></body></html>'
const parts = { font: Buffer.from('woff2 bytes'), scripts: ['addon()', 'javo()'] }

test('adds the font as an inline woff2 face and the scripts after ttyd own', () => {
  const page = buildPage(ttyd, parts)
  const face = page.slice(page.indexOf('<style>'), page.indexOf('</head>'))
  assert.match(face, /font-family:\s*"Fira Code"/)
  assert.ok(face.includes(`data:font/woff2;base64,${parts.font.toString('base64')}`))
  const ttydAt = page.indexOf('ttyd()')
  const addonAt = page.indexOf('addon()')
  const javoAt = page.indexOf('javo()')
  assert.ok(ttydAt < addonAt && addonAt < javoAt && javoAt < page.indexOf('</body>'), page)
})

test('refuses a page without exactly one head and body end', () => {
  assert.throws(() => buildPage('<html><body></body></html>', parts), /<\/head>/)
  assert.throws(() => buildPage(ttyd.replace('</body>', '</body></body>'), parts), /<\/body>/)
})

test('refuses a script that would end its script element early', () => {
  assert.throws(() => buildPage(ttyd, { ...parts, scripts: ['x = "</script>"'] }), /<\/script/)
})

test('keeps a script with replacement patterns such as $& unchanged', () => {
  const script = "s.replace(/x/, \"$&$'$`\")"
  assert.ok(buildPage(ttyd, { ...parts, scripts: [script] }).includes(`<script>${script}</script>`))
})
