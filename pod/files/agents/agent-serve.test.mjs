import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, realpathSync, rmSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir, userInfo } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

const dir = import.meta.dirname
const temp = () => mkdtempSync(join(tmpdir(), 'as-'))

// serve runs agent-serve.sh with a tmux and a ttyd that only log where tmux
// is told to keep its socket (TMUX_TMPDIR) and their arguments. It returns
// the two lines: the detached session, then ttyd with its per-tab command.
function serve(env = {}) {
  const bin = temp()
  const log = join(bin, 'calls')
  const body = (name) =>
    `#!/bin/sh\n{ printf '%s <%s>' ${name} "\${TMUX_TMPDIR-unset}"; for a; do printf ' [%s]' "$a"; done; echo; } >>${log}\n`
  for (const name of ['tmux', 'ttyd']) writeFileSync(join(bin, name), body(name), { mode: 0o755 })
  execFileSync('sh', [join(dir, 'agent-serve.sh'), '/work', 'agent; exec bash', 'ttyd', '--port=1'], {
    env: { PATH: `${bin}:${process.env.PATH}`, ...env },
  })
  return readFileSync(log, 'utf8').trim().split('\n')
}

const session = (flag) => `[-f] [/opt/javo/agents/tmux.conf] [new-session] [${flag}] [-s] [agent] [-c] [/work] [agent; exec bash]`

// A container is told nothing about tmux's socket: tmux runs as it always
// has, with its socket in the container's own /tmp.
test('in a container tmux is run with its own socket folder', () => {
  assert.deepEqual(serve(), [
    `tmux <unset> ${session('-d')}`,
    `ttyd <unset> [--port=1] [tmux] ${session('-A')}`,
  ])
})

// tmux resolves links in the folder and puts tmux-UID/default in it.
const socketOf = (folder) => join(realpathSync(folder), `tmux-${userInfo().uid}`, 'default')

// On a host the plan names the workload's own folder, and tmux is run the
// same way with it.
test('on a host tmux keeps its socket in the folder the plan names', () => {
  const folder = temp()
  assert.ok(Buffer.byteLength(socketOf(folder)) <= 103, `${folder} is too deep for this test`)
  assert.deepEqual(serve({ TMUX_TMPDIR: folder }), [
    `tmux <${folder}> ${session('-d')}`,
    `ttyd <${folder}> [--port=1] [tmux] ${session('-A')}`,
  ])
})

// A socket's path holds 103 bytes on macOS (107 on Linux), and tmux refuses
// a longer one. A folder that deep is replaced by a new folder in /tmp that
// only this user can open.
for (const [what, extra] of [['one byte too deep', 1], ['far too deep', 80]]) {
  test(`a folder ${what} for a socket is replaced by a short one`, () => {
    const base = temp()
    const folder = join(base, 'd'.repeat(103 - Buffer.byteLength(socketOf(base)) - 1 + extra))
    mkdirSync(folder)
    assert.equal(Buffer.byteLength(socketOf(folder)), 103 + extra)
    const [first, second] = serve({ TMUX_TMPDIR: folder })
    const used = first.match(/^tmux <(\/tmp\/javo-sock\.[^>]+)> /)?.[1]
    assert.ok(used, first)
    try {
      assert.equal(first, `tmux <${used}> ${session('-d')}`)
      assert.equal(second, `ttyd <${used}> [--port=1] [tmux] ${session('-A')}`)
      assert.ok(Buffer.byteLength(socketOf(used)) <= 103)
      const st = statSync(used)
      assert.ok(st.isDirectory())
      assert.equal(st.mode & 0o777, 0o700)
      assert.equal(st.uid, userInfo().uid)
    } finally {
      rmSync(used, { recursive: true })
    }
  })
}

// The deepest folder that still holds a socket is used as it is.
test('a folder that just holds a socket is kept', () => {
  const base = temp()
  const folder = join(base, 'd'.repeat(103 - Buffer.byteLength(socketOf(base)) - 1))
  mkdirSync(folder)
  assert.equal(Buffer.byteLength(socketOf(folder)), 103)
  assert.equal(serve({ TMUX_TMPDIR: folder })[0], `tmux <${folder}> ${session('-d')}`)
})
