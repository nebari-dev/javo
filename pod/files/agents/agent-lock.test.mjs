import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readFileSync, statSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

const dir = import.meta.dirname
const temp = () => mkdtempSync(join(tmpdir(), 'agent-lock-'))

// lock runs agent-lock.sh FOLDER -- COMMAND... with HOME home, from a copy
// of the pod files whose bin/javo-sandbox only records what it was asked
// to grant; returns the --rw and --ro paths and the command it would run.
function lock(home, folder, ...command) {
  return lockWith({}, home, folder, ...command)
}

// lockWith is lock with the variables env set besides HOME.
function lockWith(env, home, folder, ...command) {
  const opt = temp()
  mkdirSync(join(opt, 'agents'))
  mkdirSync(join(opt, 'bin'))
  copyFileSync(join(dir, 'agent-lock.sh'), join(opt, 'agents', 'agent-lock.sh'))
  const calls = join(opt, 'calls')
  writeFileSync(join(opt, 'bin', 'javo-sandbox'), `#!/bin/sh\nfor a; do printf '%s\\n' "$a"; done > ${calls}\n`, { mode: 0o755 })
  // Run as the pod runs it: the file itself, so it must be executable.
  execFileSync(join(opt, 'agents', 'agent-lock.sh'), [folder, '--', ...command], { env: { ...process.env, HOME: home, ...env } })
  const argv = readFileSync(calls, 'utf8').split('\n').slice(0, -1)
  const sep = argv.indexOf('--')
  const got = { rw: [], ro: [], command: argv.slice(sep + 1) }
  for (let i = 0; i < sep; i += 2) got[argv[i].slice(2)].push(argv[i + 1])
  return got
}

const covers = (granted, p) => granted.some((g) => p === g || p.startsWith(g.replace(/\/$/, '') + '/'))

// The agent may write its project and nowhere else in the home: no grant
// reaches the private keys or another project.
test('grants the project folder, creating it, and not the rest of the home', () => {
  const home = temp()
  const project = join(home, 'projects', 'api')
  const got = lock(home, project, 'claude')
  assert.ok(statSync(project).isDirectory(), 'the project folder was not made')
  assert.ok(got.rw.includes(project), `--rw ${got.rw}`)
  for (const p of [join(home, '.ssh', 'id_ed25519'), join(home, 'projects', 'other', 'main.go'), join(home, '.bashrc')]) {
    assert.ok(!covers([...got.rw, ...got.ro], p), `${p} is reachable: --rw ${got.rw} --ro ${got.ro}`)
  }
})

// Claude Code, pi and herdr (any agent may run in another's terminal) keep
// their sign-in and settings where they always do, from the first start.
test('lets the agents keep their own state, made on the first start', () => {
  const home = temp()
  const got = lock(home, join(home, 'p'), 'claude')
  for (const d of ['.claude', '.pi', join('.config', 'herdr'), join('.javo', 'agent-prompts')]) {
    assert.ok(statSync(join(home, d)).isDirectory(), `${d} was not made`)
    assert.ok(got.rw.includes(join(home, d)), `${d} is not writable: --rw ${got.rw}`)
  }
  // Claude Code keeps its settings in a file of the home itself; a file
  // the lock can grant must exist, and an empty one is not valid JSON.
  assert.equal(readFileSync(join(home, '.claude.json'), 'utf8'), '{}\n')
  assert.ok(got.rw.includes(join(home, '.claude.json')))
})

test('keeps an existing Claude Code settings file as it is', () => {
  const home = temp()
  writeFileSync(join(home, '.claude.json'), '{"theme":"dark"}')
  lock(home, join(home, 'p'), 'claude')
  assert.equal(readFileSync(join(home, '.claude.json'), 'utf8'), '{"theme":"dark"}')
})

// javo-sandbox refuses a path that does not exist, so what may be missing
// (the person's git settings, /lib64 on arm64) is granted only when there.
test('grants git settings to read only when they exist', () => {
  const home = temp()
  assert.ok(!lock(home, join(home, 'p'), 'git').ro.includes(join(home, '.gitconfig')))
  writeFileSync(join(home, '.gitconfig'), '[user]\n  name = alice\n')
  const got = lock(home, join(home, 'p'), 'git')
  assert.ok(got.ro.includes(join(home, '.gitconfig')))
  for (const p of [...got.rw, ...got.ro]) assert.ok(existsSync(p), `${p} is granted but missing`)
})

test('runs the command as given', () => {
  const home = temp()
  const got = lock(home, join(home, 'p'), '/opt/javo/pod-user.sh', 'sh', '-c', 'echo "a b"; exit 0')
  assert.deepEqual(got.command, ['/opt/javo/pod-user.sh', 'sh', '-c', 'echo "a b"; exit 0'])
})

// An app that runs as a process of a machine is told where that machine
// has the platform's files, its environment's activation and tmux's
// sockets (JAVO_FILES_DIR, JAVO_ENV_DIR, TMUX_TMPDIR): those are granted in
// place of a container's /opt/javo and /opt/javo-env.
test('on a host grants the folders the app is told of', () => {
  const home = temp()
  const files = temp()
  const env = temp()
  const tmux = join(temp(), 'tmp')
  const got = lockWith({ JAVO_FILES_DIR: files, JAVO_ENV_DIR: env, TMUX_TMPDIR: tmux }, home, join(home, 'p'), 'claude')
  assert.ok(got.ro.includes(files), `--ro ${got.ro}`)
  assert.ok(got.ro.includes(env), `--ro ${got.ro}`)
  assert.ok(statSync(tmux).isDirectory(), 'the folder of tmux was not made')
  assert.ok(got.rw.includes(tmux), `--rw ${got.rw}`)
  assert.ok(!got.rw.includes(files) && !got.rw.includes(env), `--rw ${got.rw}`)
})

test('in a container grants nothing a host is told of', () => {
  const home = temp()
  const { JAVO_FILES_DIR, JAVO_ENV_DIR, TMUX_TMPDIR, ...rest } = process.env
  const saved = process.env
  process.env = rest
  try {
    const got = lock(home, join(home, 'p'), 'claude')
    assert.deepEqual(got.rw.filter((p) => !p.startsWith(home) && p !== '/tmp' && p !== '/dev'), [])
  } finally {
    process.env = saved
  }
})
