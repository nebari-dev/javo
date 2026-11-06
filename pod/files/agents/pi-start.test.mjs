import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

const dir = import.meta.dirname
const temp = () => mkdtempSync(join(tmpdir(), 'pi-start-'))

// start runs pi-start.sh as the agent's tmux window would, with a pi and a
// bash that only log their arguments, and node running this folder's
// pi-setup.mjs in place of the image's. It returns pi's arguments.
function start(home, env = {}) {
  const bin = temp()
  const log = join(bin, 'calls')
  const stub = (name, body) => writeFileSync(join(bin, name), `#!/bin/sh\n${body}\n`, { mode: 0o755 })
  stub('node', `a=$1; shift; [ "$a" = /opt/javo/agents/pi-setup.mjs ] && a=${join(dir, 'pi-setup.mjs')}; exec ${process.execPath} "$a" "$@"`)
  stub('pi', `for a; do printf '[%s]' "$a"; done >>${log}; echo >>${log}`)
  stub('bash', 'exit 0')
  execFileSync('sh', [join(dir, 'pi-start.sh')], {
    env: { PATH: `${bin}:${process.env.PATH}`, HOME: home, HOSTNAME: 'pi-app-1', ...env },
  })
  return readFileSync(log, 'utf8').trim()
}

const THEME = '[--use-theme][light/dark]'

// The first prompt is sent once; every later start reopens pi's most recent
// session in the folder (pi 1.4.0 --continue, which starts a new session
// when the folder has none), so a restarted app shows its conversation.
test('sends the first prompt once, then continues the session', () => {
  const home = temp()
  const env = { JAVO_AGENT_PROMPT: 'add a README' }
  assert.equal(start(home, env), `${THEME}[--][add a README]`)
  assert.equal(start(home, env), `${THEME}[--continue]`)
})

test('continues the session of an app with no first prompt', () => {
  assert.equal(start(temp()), `${THEME}[--continue]`)
})
