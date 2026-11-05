#!/usr/bin/env node
// pi-setup sets up the pi coding agent of a Javo coding-agent app (the pi
// template's start script, pi-start.sh, runs it):
//
//   pi-setup.mjs              point pi at the app's model (JAVO_AGENT_MODE,
//                             JAVO_AGENT_MODEL, JAVO_AGENT_BASE_URL)
//   pi-setup.mjs first-start  exit 0 the first time this app starts with its
//                             first prompt (JAVO_AGENT_PROMPT), 1 after
//
// It writes pi's models.json, settings.json (pi 1.4.0, docs/models.md and
// docs/settings.md) and APPEND_SYSTEM.md (see ownAppendSystem) in the agent
// folder ($PI_CODING_AGENT_DIR, default ~/.pi/agent). The javo provider in
// models.json is always pi-setup's: it is rewritten on every start, so code
// that can write the home cannot point it at another server, and no other
// provider in models.json or auth.json may name $JAVO_AGENT_KEY. Everything
// else in those files is the person's and is kept. settings.json is written
// only when it is missing or still what pi-setup wrote last (its sha256 is
// kept in .javo-managed.json), and not once pi is signed in (auth.json). The
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
    provider,
    'settings.json': JSON.stringify({ defaultProvider: PROVIDER, defaultModel: env.JAVO_AGENT_MODEL }, null, 2) + '\n',
  }
}

const KEY = 'JAVO_AGENT_KEY'
const isObject = (v) => v !== null && typeof v === 'object' && !Array.isArray(v)

// dropKeyRefs removes, in place, every string in v that names the key ($NAME,
// ${NAME} or a !command), so no provider but javo's can be given it.
function dropKeyRefs(v) {
  if (Array.isArray(v)) {
    for (let i = v.length - 1; i >= 0; i--) {
      if (typeof v[i] === 'string' && v[i].includes(KEY)) v.splice(i, 1)
      else dropKeyRefs(v[i])
    }
  } else if (isObject(v)) {
    for (const [k, x] of Object.entries(v)) {
      if (typeof x === 'string' && x.includes(KEY)) delete v[k]
      else dropKeyRefs(x)
    }
  }
}

const stripBom = (s) => (s.charCodeAt(0) === 0xfeff ? s.slice(1) : s)

function writePrivate(dir, path, content) {
  mkdirSync(dir, { recursive: true, mode: 0o700 })
  writeFileSync(path, content, { mode: 0o600 })
}

// ownModels makes models.json's javo provider exactly provider and drops the
// key from every other provider; it creates the file only when create is
// set. A file pi-setup cannot read as JSON (pi also takes comments) is
// replaced, its text kept in models.json.javo-bak. It returns what the file
// now holds, or undefined when there is none.
function ownModels(dir, provider, create) {
  const path = join(dir, 'models.json')
  const cur = readText(path)
  if (cur === undefined && !create) return undefined
  let models
  try {
    models = cur === undefined ? {} : JSON.parse(stripBom(cur))
  } catch {
    models = undefined
  }
  if (!isObject(models) || (models.providers !== undefined && !isObject(models.providers))) {
    if (cur !== undefined) writePrivate(dir, path + '.javo-bak', cur)
    models = {}
  }
  models.providers ??= {}
  delete models.providers[PROVIDER]
  dropKeyRefs(models)
  models.providers[PROVIDER] = provider
  const content = JSON.stringify(models, null, 2) + '\n'
  if (content !== cur) writePrivate(dir, path, content)
  return content
}

// ownAuth drops a stored javo credential (it would replace the key) and the
// key from every other stored credential. pi reads auth.json as plain JSON,
// so a file that does not parse gives pi nothing either.
function ownAuth(dir) {
  const path = join(dir, 'auth.json')
  const cur = readText(path)
  let auth
  try {
    auth = JSON.parse(stripBom(cur ?? ''))
  } catch {
    return
  }
  if (!isObject(auth)) return
  const before = JSON.stringify(auth)
  delete auth[PROVIDER]
  dropKeyRefs(auth)
  if (JSON.stringify(auth) !== before) writePrivate(dir, path, JSON.stringify(auth, null, 2) + '\n')
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

// nextSettings writes settings.json while it is pi-setup's (removes it when
// there is no model) and returns what it wrote.
function nextSettings(dir, want, managed) {
  const name = 'settings.json'
  const path = join(dir, name)
  const cur = readText(path)
  let content = want?.[name]
  if (cur !== undefined && managed[name] !== sha(cur)) {
    // pi edits its settings itself; they stay ours while it defaults to javo.
    if (!managed[name] || !want) return undefined
    content = ourSettings(cur, want[name])
    if (content === undefined) return undefined
  }
  if (!want) {
    if (cur !== undefined) rmSync(path)
    return undefined
  }
  writePrivate(dir, path, content)
  return content
}

// pi shows what its write and edit tools put in a file with syntax
// highlighting, and a bash command (a heredoc's file included) as plain text
// (pi 1.4.0, core/tools/renderers/write.js and bash.js). APPEND_SYSTEM.md in
// the agent folder is added to pi's system prompt (docs/configuration.md);
// pi-system.md asks pi to write files with those tools. It is written while
// it is missing or still what pi-setup wrote last, so a person's own file is
// kept. Not the --append-system-prompt flag: that would hide the person's
// APPEND_SYSTEM.md from pi.
const APPEND_SYSTEM = 'APPEND_SYSTEM.md'
const appendSystem = () => readFileSync(new URL('./pi-system.md', import.meta.url), 'utf8')

function ownAppendSystem(dir, managed) {
  const path = join(dir, APPEND_SYSTEM)
  const cur = readText(path)
  if (cur !== undefined && managed[APPEND_SYSTEM] !== sha(cur)) return undefined
  const content = appendSystem()
  if (content !== cur) writePrivate(dir, path, content)
  return content
}

export function configure(env) {
  const dir = agentDir(env)
  const want = wanted(env)
  if (want) ownAuth(dir)
  const signed = signedIn(dir)
  let managed = {}
  try {
    managed = JSON.parse(readText(join(dir, MANAGED)) ?? '{}')
  } catch {
    managed = {}
  }
  const next = {}
  if (want) {
    const models = ownModels(dir, want.provider, !signed)
    if (models !== undefined) next['models.json'] = sha(models)
  } else {
    const path = join(dir, 'models.json')
    const cur = readText(path)
    if (cur !== undefined && managed['models.json'] === sha(cur)) rmSync(path)
  }
  const settings = signed ? undefined : nextSettings(dir, want, managed)
  if (settings !== undefined) next['settings.json'] = sha(settings)
  const append = ownAppendSystem(dir, managed)
  if (append !== undefined) next[APPEND_SYSTEM] = sha(append)
  if (Object.keys(next).length > 0 || existsSync(join(dir, MANAGED))) {
    writePrivate(dir, join(dir, MANAGED), JSON.stringify(next) + '\n')
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
