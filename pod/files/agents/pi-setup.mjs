#!/usr/bin/env node
// pi-setup sets up the pi coding agent of a Javo coding-agent app (the pi
// template's start script, pi-start.sh, runs it):
//
//   pi-setup.mjs              point pi at the app's model (JAVO_AGENT_MODE,
//                             JAVO_AGENT_MODEL, JAVO_AGENT_BASE_URL)
//   pi-setup.mjs first-start  exit 0 the first time this app starts with its
//                             first prompt (JAVO_AGENT_PROMPT), 1 after
//
// It writes pi's models.json and settings.json (pi 1.4.0, docs/models.md and
// docs/settings.md) in the agent folder ($PI_CODING_AGENT_DIR, default
// ~/.pi/agent) only when they are missing or still exactly what it wrote
// last (their sha256 is kept in .javo-managed.json), and nothing once pi is
// signed in (auth.json): a person's own pi setup is never overwritten. The
// key is never written: models.json names $JAVO_AGENT_KEY and pi reads the
// variable when it sends a request.
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'
import { pathToFileURL } from 'node:url'

const PROVIDER = 'javo'
const MANAGED = '.javo-managed.json'

const sha = (s) => createHash('sha256').update(s).digest('hex')
const agentDir = (env) => env.PI_CODING_AGENT_DIR || join(env.HOME, '.pi', 'agent')

function readText(p) {
  try {
    return readFileSync(p, 'utf8')
  } catch {
    return undefined
  }
}

// The files pi should have for env, or null for none.
function wanted(env) {
  const mode = env.JAVO_AGENT_MODE
  if ((mode !== 'platform' && mode !== 'own') || !env.JAVO_AGENT_MODEL || !env.JAVO_AGENT_BASE_URL) return null
  const provider = {
    name: 'Javo',
    baseUrl: env.JAVO_AGENT_BASE_URL,
    api: 'openai-completions',
    apiKey: '$JAVO_AGENT_KEY',
    models: [{ id: env.JAVO_AGENT_MODEL, name: env.JAVO_AGENT_MODEL, maxTokens: 16384 }],
  }
  // Behind the gateway pi cannot tell the provider from the address, so it
  // sends only what every OpenAI-compatible server takes. With the person's
  // own key it sees the real address and works it out itself.
  if (mode === 'platform') {
    provider.compat = { supportsStore: false, supportsDeveloperRole: false, supportsReasoningEffort: false, maxTokensField: 'max_tokens' }
  }
  return {
    'models.json': JSON.stringify({ providers: { [PROVIDER]: provider } }, null, 2) + '\n',
    'settings.json': JSON.stringify({ defaultProvider: PROVIDER, defaultModel: env.JAVO_AGENT_MODEL }, null, 2) + '\n',
  }
}

// signedIn is whether pi holds credentials of its own (/login): pi writes an
// empty auth file at its first start.
function signedIn(dir) {
  try {
    return Object.keys(JSON.parse(readText(join(dir, 'auth.json')) ?? '{}')).length > 0
  } catch {
    return true
  }
}

// ourSettings is cur with pi's default model set to want's, while pi still
// defaults to the javo provider (or names none); undefined when the person
// picked another provider. pi adds keys of its own (lastChangelogVersion),
// which are kept.
function ourSettings(cur, want) {
  let s
  try {
    s = JSON.parse(cur)
  } catch {
    return undefined
  }
  if (s.defaultProvider !== undefined && s.defaultProvider !== PROVIDER) return undefined
  return JSON.stringify({ ...s, ...JSON.parse(want) }, null, 2) + '\n'
}

export function configure(env) {
  const dir = agentDir(env)
  if (signedIn(dir)) return
  let managed = {}
  try {
    managed = JSON.parse(readText(join(dir, MANAGED)) ?? '{}')
  } catch {
    managed = {}
  }
  const want = wanted(env)
  const next = {}
  for (const name of ['models.json', 'settings.json']) {
    const path = join(dir, name)
    const cur = readText(path)
    let content = want?.[name]
    const ours = cur === undefined || managed[name] === sha(cur)
    if (!ours) {
      // pi edits its settings itself; they stay ours while it defaults to javo.
      if (name !== 'settings.json' || !managed[name] || !want) continue
      content = ourSettings(cur, want[name])
      if (content === undefined) continue
    }
    if (want) {
      mkdirSync(dir, { recursive: true, mode: 0o700 })
      writeFileSync(path, content, { mode: 0o600 })
      next[name] = sha(content)
    } else if (cur !== undefined) {
      rmSync(path)
    }
  }
  if (Object.keys(next).length > 0 || existsSync(join(dir, MANAGED))) {
    mkdirSync(dir, { recursive: true, mode: 0o700 })
    writeFileSync(join(dir, MANAGED), JSON.stringify(next) + '\n', { mode: 0o600 })
  }
}

// themeArgs are pi's flags for its colours. pi's default theme, "system",
// builds its palette from the colours the terminal reports; behind tmux part
// of that answer never comes, and pi stays in its grey placeholder, code
// included. So pi follows the page's light or dark look with its own
// palettes, unless the person picked a theme (settings.json "theme").
export function themeArgs(env) {
  try {
    if (JSON.parse(readText(join(agentDir(env), 'settings.json')) ?? '{}').theme) return []
  } catch {
    // An unreadable settings file names no theme.
  }
  return ['--use-theme', 'light/dark']
}

// firstStart reports whether this app (its pod's host name, which is the
// app's) has not yet run this first prompt, and records that it now has.
export function firstStart(env) {
  const prompt = env.JAVO_AGENT_PROMPT
  if (!prompt) return false
  const dir = join(env.HOME, '.javo', 'agent-prompts')
  const marker = join(dir, `${env.HOSTNAME || 'app'}-${sha(prompt).slice(0, 16)}`)
  if (existsSync(marker)) return false
  mkdirSync(dir, { recursive: true, mode: 0o700 })
  writeFileSync(marker, new Date().toISOString() + '\n', { mode: 0o600 })
  return true
}

if (import.meta.url === pathToFileURL(process.argv[1] ?? '').href) {
  try {
    if (process.argv[2] === 'first-start') {
      process.exit(firstStart(process.env) ? 0 : 1)
    }
    if (process.argv[2] === 'theme-args') {
      process.stdout.write(themeArgs(process.env).join(' ') + '\n')
      process.exit(0)
    }
    configure(process.env)
  } catch (err) {
    process.stderr.write(`javo: could not set up pi's model (${err.message}); use /login in pi instead.\n`)
    process.exit(process.argv[2] === 'first-start' ? 1 : 0)
  }
}
