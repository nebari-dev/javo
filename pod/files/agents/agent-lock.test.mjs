import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { copyFileSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

const dir = import.meta.dirname
// The stand-in homes are kept out of /tmp, which the script grants: on Linux
// the system's temporary folder is /tmp, and a home inside it would be
// reachable whatever else the script does. .dev is ignored by git.
const base = tmpdir().startsWith('/tmp') ? join(dir, '..', '..', '..', '.dev', 'test-tmp') : tmpdir()
mkdirSync(base, { recursive: true })
const temp = () => mkdtempSync(join(base, 'agent-lock-'))

// lock runs agent-lock.sh FOLDER -- COMMAND... with HOME home, from a copy
// of the pod files whose bin/javo-sandbox only records what it was asked
// to grant; returns the --rw and --ro paths, those of the --rw it is to
// make (--rw-make), the folders it is to refuse links under
// (--no-links-under) and the command it would run.
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
  execFileSync(join(opt, 'agents', 'agent-lock.sh'), [folder, '--', ...command], { env: { ...process.env, HOME: home, ...env }, stdio: 'pipe' })
  const argv = readFileSync(calls, 'utf8').split('\n').slice(0, -1)
  const sep = argv.indexOf('--')
  const got = { rw: [], ro: [], make: [], noLinks: [], command: argv.slice(sep + 1) }
  const lists = { '--rw': [got.rw], '--ro': [got.ro], '--rw-make': [got.rw, got.make], '--no-links-under': [got.noLinks] }
  for (let i = 0; i < sep; i += 2) for (const list of lists[argv[i]]) list.push(argv[i + 1])
  return got
}

const covers = (granted, p) => granted.some((g) => p === g || p.startsWith(g.replace(/\/$/, '') + '/'))

// The agent may write its project and nowhere else in the home: no grant
// reaches the private keys or another project.
test('grants the project folder, to be made, and not the rest of the home', () => {
  const home = temp()
  const project = join(home, 'projects', 'api')
  const got = lock(home, project, 'claude')
  assert.ok(got.make.includes(project), `--rw-make ${got.make}`)
  for (const p of [join(home, '.ssh', 'id_ed25519'), join(home, 'projects', 'other', 'main.go'), join(home, '.bashrc')]) {
    assert.ok(!covers([...got.rw, ...got.ro], p), `${p} is reachable: --rw ${got.rw} --ro ${got.ro}`)
  }
})

// A locked agent may make links in what it is granted, so a link under the
// home may lead anywhere in it (to ~/.ssh, to the home itself). The script
// follows none: javo-sandbox is told to refuse a granted path under the
// home that is, or is reached through, a link, and makes the missing
// folders itself, as it opens them.
test('has javo-sandbox refuse links under the home, and writes nothing through one', () => {
  const home = temp()
  const project = join(home, 'projects', 'api')
  mkdirSync(join(home, '.ssh'))
  mkdirSync(project, { recursive: true })
  symlinkSync(join(home, '.ssh'), join(project, 'sub'))
  symlinkSync(join(home, '.ssh'), join(home, '.claude'))
  symlinkSync(join(home, '.ssh', 'new'), join(home, '.claude.json'))
  const got = lock(home, join(project, 'sub', 'planted'), 'claude')
  assert.deepEqual(got.noLinks, [home])
  assert.deepEqual(readdirSync(join(home, '.ssh')), [], 'the script wrote through a link')
})

// Locked to the home, or to a folder the home is in, the lock would keep
// nothing out; and a folder that is not a whole path from / is not one the
// platform asked for.
test('refuses a folder that is the home, holds it, or is not a plain path', () => {
  const home = temp()
  for (const folder of [home, home + '/', join(home, '..'), '/', home + '/projects/../.ssh', home + '/./.ssh', 'projects/api', '']) {
    assert.throws(
      () => lock(home, folder, 'claude'),
      (err) => err.status === 125 && err.stderr.toString().includes(`(${folder})`),
      `folder ${JSON.stringify(folder)} was not refused`,
    )
  }
})

// Claude Code, pi, herdr and Harf (any agent may run in another's terminal)
// keep their sign-in, settings and chats where they always do, from the
// first start.
test('lets the agents keep their own state, made on the first start', () => {
  const home = temp()
  const got = lock(home, join(home, 'p'), 'claude')
  for (const d of ['.claude', '.pi', '.harf', join('.config', 'herdr'), join('.javo', 'agent-prompts')]) {
    assert.ok(got.make.includes(join(home, d)), `${d} is not writable, or not made: --rw-make ${got.make}`)
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
  for (const p of [...got.rw, ...got.ro]) assert.ok(got.make.includes(p) || existsSync(p), `${p} is granted but missing`)
})

test('runs the command as given', () => {
  const home = temp()
  const got = lock(home, join(home, 'p'), '/opt/javo/pod-user.sh', 'sh', '-c', 'echo "a b"; exit 0')
  assert.deepEqual(got.command, ['/opt/javo/pod-user.sh', 'sh', '-c', 'echo "a b"; exit 0'])
})

// An app that runs as a process of a machine is told where that machine
// has the platform's files, its environment's activation and tmux's
// sockets (JAVO_FILES_DIR, JAVO_ENV_DIR, TMUX_TMPDIR) and its scratch
// folder, where the tools keep their caches and environments
// (JAVO_SCRATCH_DIR): those are granted in place of a container's
// /opt/javo, /opt/javo-env and /scratch.
test('on a host grants the folders the app is told of', () => {
  const home = temp()
  const files = temp()
  const env = temp()
  const tmux = join(temp(), 'tmp')
  const scratch = join(temp(), 'scratch')
  const got = lockWith({ JAVO_FILES_DIR: files, JAVO_ENV_DIR: env, TMUX_TMPDIR: tmux, JAVO_SCRATCH_DIR: scratch }, home, join(home, 'p'), 'claude')
  assert.ok(got.make.includes(scratch), `--rw-make ${got.make}`)
  assert.ok(got.ro.includes(files), `--ro ${got.ro}`)
  assert.ok(got.ro.includes(env), `--ro ${got.ro}`)
  assert.ok(got.make.includes(tmux), `--rw-make ${got.make}`)
  assert.ok(!got.rw.includes(files) && !got.rw.includes(env), `--rw ${got.rw}`)
})

// In a container ~/.ssh is the pod's own folder of public files. On a host
// no sidecar keeps the keys apart: ~/.ssh there is the person's own, with
// the private keys, and is not granted.
test('on a host does not grant ~/.ssh, which holds the private keys there', () => {
  const home = temp()
  mkdirSync(join(home, '.ssh'))
  writeFileSync(join(home, '.ssh', 'a-private-key'), 'key')
  assert.ok(lock(home, join(home, 'p'), 'git').ro.includes(join(home, '.ssh')), 'a container reads its ~/.ssh')
  const got = lockWith({ JAVO_FILES_DIR: temp() }, home, join(home, 'p'), 'git')
  assert.ok(!covers([...got.rw, ...got.ro], join(home, '.ssh', 'a-private-key')), `--rw ${got.rw} --ro ${got.ro}`)
})

// The platform's own variables (JAVO_...) are not an app's to set;
// TMUX_TMPDIR is, and a container is told none by the platform. One set
// there is the app's own, and is not a folder to grant: it could name the
// home.
test('in a container does not grant the folder an app names for tmux', () => {
  const home = temp()
  const got = lockWith({ TMUX_TMPDIR: home }, home, join(home, 'p'), 'claude')
  assert.ok(!covers([...got.rw, ...got.ro], join(home, '.bashrc')), `--rw ${got.rw} --ro ${got.ro}`)
})

// What else a pod mounts for the app: its scratch volume, the pod's own, to
// write; the clone of the repository it runs from and the Nebi CLI and
// token to read. The folders of the person's groups (/shared) hold other
// people's files and are not granted. Those paths are the system's, so
// this runs where they can be made: as root in a container.
test('grants the pod its scratch, and the clone and Nebi to read, but no group folder', (t) => {
  const made = []
  t.after(() => made.reverse().forEach((d) => rmSync(d, { recursive: true, force: true })))
  const group = '/shared/team'
  try {
    for (const d of ['/scratch', '/srv/src', '/opt/javo-nebi', '/var/run/javo/nebi', group]) {
      const first = mkdirSync(d, { recursive: true })
      if (first) made.push(first)
    }
  } catch {
    return t.skip('cannot make folders in / here')
  }
  const home = temp()
  const got = lock(home, join(home, 'p'), 'claude')
  assert.ok(got.rw.includes('/scratch'), `--rw ${got.rw}`)
  for (const d of ['/srv/src', '/opt/javo-nebi', '/var/run/javo/nebi']) {
    assert.ok(got.ro.includes(d), `${d} is not readable: --ro ${got.ro}`)
    assert.ok(!covers(got.rw, d), `${d} is writable: --rw ${got.rw}`)
  }
  assert.ok(!covers([...got.rw, ...got.ro], group), `${group} is reachable: --rw ${got.rw} --ro ${got.ro}`)
})

test('in a container grants nothing a host is told of', () => {
  const home = temp()
  const { JAVO_FILES_DIR, JAVO_ENV_DIR, JAVO_SCRATCH_DIR, TMUX_TMPDIR, ...rest } = process.env
  const saved = process.env
  process.env = rest
  try {
    const got = lock(home, join(home, 'p'), 'claude')
    assert.deepEqual(got.rw.filter((p) => !p.startsWith(home) && p !== '/tmp' && p !== '/dev' && p !== '/scratch'), [])
  } finally {
    process.env = saved
  }
})
