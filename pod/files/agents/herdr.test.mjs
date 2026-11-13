import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir, userInfo } from 'node:os'
import { dirname, join } from 'node:path'
import { test } from 'node:test'

const dir = import.meta.dirname
const temp = () => mkdtempSync(join(tmpdir(), 'hs-'))

// start runs herdr.sh as ttyd does for a tab, with a herdr that only logs
// where it is told to keep its server's socket (HERDR_SOCKET_PATH) and its
// arguments. It returns that line.
function start(home, env = {}) {
  const bin = temp()
  const log = join(bin, 'calls')
  writeFileSync(
    join(bin, 'herdr'),
    `#!/bin/sh\n{ printf 'herdr <%s>' "\${HERDR_SOCKET_PATH-unset}"; for a; do printf ' [%s]' "$a"; done; echo; } >>${log}\n`,
    { mode: 0o755 },
  )
  execFileSync('sh', [join(dir, 'herdr.sh'), '--session', 'main'], {
    env: { PATH: `${bin}:${process.env.PATH}`, HOME: home, ...env },
  })
  return readFileSync(log, 'utf8').trim()
}

// In a container herdr is told nothing: its socket is beside its config in
// the home, as it always was.
test('in a container herdr keeps its socket where it always did', () => {
  // A home that has its settings: the platform's own are in /opt/javo there.
  const home = temp()
  mkdirSync(join(home, '.config/herdr'), { recursive: true })
  writeFileSync(join(home, '.config/herdr/config.toml'), '')
  assert.equal(start(home, { TMPDIR: temp() }), 'herdr <unset> [--session] [main]')
})

// On a host the home is the one every app of the person shares, so one
// app's herdr would be a client of another's server, and end with it. Each
// app keeps its socket in its own temporary folder.
test('on a host herdr keeps its socket in the workload\'s temporary folder', () => {
  const home = temp()
  const folder = temp()
  assert.ok(Buffer.byteLength(join(realpathSync(folder), 'herdr-client.sock')) <= 103, `${folder} is too deep for this test`)
  const env = { JAVO_FILES_DIR: dirname(dir), TMPDIR: folder }
  assert.equal(start(home, env), `herdr <${folder}/herdr.sock> [--session] [main]`)
  // The platform's settings still go to the home on the first start.
  assert.ok(existsSync(join(home, '.config/herdr/config.toml')))
})

// A socket's path holds 103 bytes on macOS (107 on Linux). A temporary
// folder too deep for herdr's two sockets (herdr.sock, herdr-client.sock)
// is replaced by a new folder in /tmp that only this user can open.
test('a temporary folder too deep for a socket is replaced by a short one', () => {
  const base = temp()
  const folder = join(base, 'd'.repeat(103 - Buffer.byteLength(join(realpathSync(base), 'herdr-client.sock'))))
  mkdirSync(folder)
  assert.equal(Buffer.byteLength(join(realpathSync(folder), 'herdr-client.sock')), 104)
  const home = temp()
  const line = start(home, { JAVO_FILES_DIR: dirname(dir), TMPDIR: folder })
  const used = line.match(/^herdr <(\/tmp\/javo-sock\.[^/>]+)\/herdr\.sock> /)?.[1]
  assert.ok(used, line)
  try {
    // The next tab finds the same folder, and so the same server.
    assert.equal(start(home, { JAVO_FILES_DIR: dirname(dir), TMPDIR: folder }), line)
    const st = statSync(used)
    assert.ok(st.isDirectory())
    assert.equal(st.mode & 0o777, 0o700)
    assert.equal(st.uid, userInfo().uid)
  } finally {
    rmSync(used, { recursive: true })
  }
})
