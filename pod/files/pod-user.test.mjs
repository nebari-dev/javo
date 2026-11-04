import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { test } from 'node:test'

// pod-user.sh starts every app's command, including tmux, which shows each
// non-ASCII character as "_" unless the locale says UTF-8.
const script = join(import.meta.dirname, 'pod-user.sh')
const run = (env) =>
  execFileSync('bash', [script, 'sh', '-c', 'printf %s "$LANG"'], { env: { PATH: process.env.PATH, ...env }, encoding: 'utf8' })

test('sets a UTF-8 locale when the pod sets none', { skip: !existsSync('/usr/lib/locale/C.utf8') }, () => {
  assert.equal(run({}), 'C.UTF-8')
})

test('keeps a locale the pod already sets', () => {
  assert.equal(run({ LANG: 'en_GB.UTF-8' }), 'en_GB.UTF-8')
})
