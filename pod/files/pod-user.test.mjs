import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
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

// On a machine that runs apps as processes the platform's files and the
// environment's activation are in folders the app is told of
// (JAVO_FILES_DIR and JAVO_ENV_DIR); a container has them at fixed paths.
test('reads the activation and the tools from the folders it is told of', () => {
  const dir = mkdtempSync(join(tmpdir(), 'pod-user-'))
  try {
    writeFileSync(join(dir, 'activate.sh'), 'export ACTIVATED=yes\n')
    const out = execFileSync('bash', [script, 'sh', '-c', 'printf "%s %s" "$ACTIVATED" "$PATH"'], {
      env: { PATH: process.env.PATH, JAVO_ENV_DIR: dir, JAVO_FILES_DIR: '/somewhere/files' },
      encoding: 'utf8',
    })
    assert.match(out, /^yes .*:\/somewhere\/files\/bin$/)
  } finally {
    rmSync(dir, { recursive: true })
  }
})

test('names the container paths when it is told of no folder', () => {
  const out = execFileSync('bash', [script, 'sh', '-c', 'printf %s "$PATH"'], { env: { PATH: '/usr/bin:/bin' }, encoding: 'utf8' })
  assert.equal(out, '/usr/bin:/bin:/opt/javo/bin')
})
