import assert from 'node:assert/strict'
import { execFileSync, spawn, spawnSync } from 'node:child_process'
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, symlinkSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'

const dir = import.meta.dirname
const temp = () => mkdtempSync(join(tmpdir(), 'ssh-agent-'))
const has = (cmd) => spawnSync('sh', ['-c', `command -v ${cmd}`]).status === 0
const keygen = (file, pass) => execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', pass, '-C', file, '-f', file])

// The agent container's ~/.ssh gets only what ssh needs to verify hosts and
// pick keys: never a private key, whatever its name.
test('copies known_hosts, config and public keys, never a private key', () => {
  const src = temp()
  const dst = temp()
  const priv = '-----BEGIN OPENSSH PRIVATE KEY-----\nAAAA\n-----END OPENSSH PRIVATE KEY-----\n'
  writeFileSync(join(src, 'id_ed25519'), priv)
  writeFileSync(join(src, 'id_ed25519.pub'), 'ssh-ed25519 AAAA alice@example.com\n')
  writeFileSync(join(src, 'known_hosts'), 'github.com ssh-ed25519 AAAA\n')
  writeFileSync(join(src, 'config'), 'Host github.com\n  IdentityFile ~/.ssh/id_ed25519\n')
  writeFileSync(join(src, 'deploy_key'), priv)
  // A .pub name is not enough: a link to the key, or a key under that name.
  symlinkSync(join(src, 'id_ed25519'), join(src, 'link.pub'))
  writeFileSync(join(src, 'renamed.pub'), priv)
  mkdirSync(join(src, 'sub.pub'))
  execFileSync('sh', [join(dir, 'ssh-public.sh'), src, dst])
  assert.deepEqual(readdirSync(dst).sort(), ['config', 'id_ed25519.pub', 'known_hosts'])
  assert.equal(readFileSync(join(dst, 'config'), 'utf8'), 'Host github.com\n  IdentityFile ~/.ssh/id_ed25519\n')
})

test('copies nothing and succeeds when there is no ~/.ssh', () => {
  const dst = temp()
  execFileSync('sh', [join(dir, 'ssh-public.sh'), join(temp(), 'missing'), dst])
  assert.deepEqual(readdirSync(dst), [])
})

// The sidecar loads every key it can without a passphrase, skips the rest
// with a line in its log, and never waits for input.
test('loads unencrypted keys and skips passphrase-protected ones', { skip: !has('ssh-agent') || !has('ssh-keygen') }, async () => {
  const home = temp()
  const ssh = join(home, '.ssh')
  mkdirSync(ssh)
  keygen(join(ssh, 'id_ed25519'), '')
  keygen(join(ssh, 'id_work'), 'a-passphrase-for-the-test')
  // Shared storage may leave a key readable by the group; ssh-add would
  // refuse the file itself.
  chmodSync(join(ssh, 'id_ed25519'), 0o644)
  const run = temp()
  const sock = join(run, 'agent.sock')
  const pub = temp()
  const child = spawn('sh', [join(dir, 'ssh-agent.sh'), ssh, pub, sock], { stdio: ['ignore', 'pipe', 'pipe'] })
  let log = ''
  child.stdout.on('data', (d) => (log += d))
  child.stderr.on('data', (d) => (log += d))
  try {
    for (let i = 0; i < 100 && !existsSync(join(run, 'ready')); i++) await new Promise((r) => setTimeout(r, 100))
    assert.ok(existsSync(join(run, 'ready')), `no ready marker; log:\n${log}`)
    const listed = spawnSync('ssh-add', ['-L'], { env: { ...process.env, SSH_AUTH_SOCK: sock }, encoding: 'utf8' })
    assert.equal(listed.status, 0, listed.stderr)
    const keys = listed.stdout.trim().split('\n')
    assert.equal(keys.length, 1, listed.stdout)
    assert.equal(keys[0], readFileSync(join(ssh, 'id_ed25519.pub'), 'utf8').trim())
    assert.match(log, /id_work/)
    assert.deepEqual(readdirSync(pub).sort(), ['id_ed25519.pub', 'id_work.pub'])
  } finally {
    child.kill('SIGTERM')
  }
  const code = await new Promise((r) => child.on('exit', (c, s) => r(c ?? s)))
  assert.ok(code === 0 || code === 143 || code === 'SIGTERM', `exit ${code}; log:\n${log}`)
})

// Without ssh-agent (an environment that does not bring openssh) the
// sidecar says so and stays up, so the app still starts.
test('reports a missing ssh-agent and still marks itself ready', async () => {
  // A PATH with the shell tools the script uses and no OpenSSH.
  const bin = temp()
  for (const tool of ['sh', 'cat', 'chmod', 'grep', 'mkdir', 'rm', 'sleep', 'touch']) {
    symlinkSync(execFileSync('sh', ['-c', `command -v ${tool}`], { encoding: 'utf8' }).trim(), join(bin, tool))
  }
  const run = temp()
  const child = spawn(join(bin, 'sh'), [join(dir, 'ssh-agent.sh'), join(temp(), 'none'), temp(), join(run, 'agent.sock')], {
    env: { PATH: bin },
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  let log = ''
  child.stderr.on('data', (d) => (log += d))
  try {
    for (let i = 0; i < 50 && !existsSync(join(run, 'ready')); i++) await new Promise((r) => setTimeout(r, 100))
    assert.ok(existsSync(join(run, 'ready')), `no ready marker; log:\n${log}`)
    assert.match(log, /no ssh-agent/)
    assert.equal(child.exitCode, null)
  } finally {
    child.kill('SIGTERM')
  }
})
