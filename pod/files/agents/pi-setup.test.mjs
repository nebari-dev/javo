import assert from 'node:assert/strict'
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { test } from 'node:test'
import { configure, firstStart } from './pi-setup.mjs'

const home = () => mkdtempSync(join(tmpdir(), 'pi-setup-'))
const platform = (h) => ({
  HOME: h,
  JAVO_AGENT_MODE: 'platform',
  JAVO_AGENT_MODEL: 'anthropic/claude-sonnet-5.5',
  JAVO_AGENT_BASE_URL: 'https://javo.example.com/api/ai/openai/v1',
  JAVO_AGENT_KEY: 'javo_secret-token-text',
})
const read = (h, f) => JSON.parse(readFileSync(join(h, '.pi/agent', f), 'utf8'))

test('points pi at the gateway with the key taken from the environment, never written', () => {
  const h = home()
  configure(platform(h))
  const models = read(h, 'models.json')
  const p = models.providers.javo
  assert.equal(p.baseUrl, 'https://javo.example.com/api/ai/openai/v1')
  assert.equal(p.api, 'openai-completions')
  assert.equal(p.apiKey, '$JAVO_AGENT_KEY')
  assert.deepEqual(p.models.map((m) => m.id), ['anthropic/claude-sonnet-5.5'])
  const settings = read(h, 'settings.json')
  assert.equal(settings.defaultProvider, 'javo')
  assert.equal(settings.defaultModel, 'anthropic/claude-sonnet-5.5')
  for (const f of ['models.json', 'settings.json']) {
    assert.ok(!readFileSync(join(h, '.pi/agent', f), 'utf8').includes('javo_secret-token-text'), `${f} holds the key`)
  }
})

test('follows a changed model, while the files are still the ones it wrote', () => {
  const h = home()
  configure(platform(h))
  configure({ ...platform(h), JAVO_AGENT_MODEL: 'other/model' })
  assert.equal(read(h, 'settings.json').defaultModel, 'other/model')
  assert.deepEqual(read(h, 'models.json').providers.javo.models.map((m) => m.id), ['other/model'])
})

test("never overwrites a person's own pi configuration", () => {
  const h = home()
  mkdirSync(join(h, '.pi/agent'), { recursive: true })
  writeFileSync(join(h, '.pi/agent/settings.json'), '{"defaultProvider":"anthropic","theme":"dark"}')
  configure(platform(h))
  assert.deepEqual(read(h, 'settings.json'), { defaultProvider: 'anthropic', theme: 'dark' })
  // A file it wrote and the person then edited is theirs too.
  const h2 = home()
  configure(platform(h2))
  writeFileSync(join(h2, '.pi/agent/models.json'), '{"providers":{"mine":{}}}')
  configure({ ...platform(h2), JAVO_AGENT_MODEL: 'other/model' })
  assert.deepEqual(read(h2, 'models.json'), { providers: { mine: {} } })
})

test('a signed-in pi (its own auth.json) is left alone', () => {
  const h = home()
  mkdirSync(join(h, '.pi/agent'), { recursive: true })
  writeFileSync(join(h, '.pi/agent/auth.json'), '{"anthropic":{}}')
  configure(platform(h))
  assert.ok(!existsSync(join(h, '.pi/agent/models.json')))
  assert.ok(!existsSync(join(h, '.pi/agent/settings.json')))
})

test('without a model it removes only what it wrote', () => {
  const h = home()
  configure(platform(h))
  configure({ HOME: h, JAVO_AGENT_MODE: 'login' })
  assert.ok(!existsSync(join(h, '.pi/agent/models.json')))
  assert.ok(!existsSync(join(h, '.pi/agent/settings.json')))
})

test("the person's own key keeps its provider's address, for pi to recognise it", () => {
  const h = home()
  configure({ HOME: h, JAVO_AGENT_MODE: 'own', JAVO_AGENT_MODEL: 'm', JAVO_AGENT_BASE_URL: 'https://openrouter.ai/api/v1', JAVO_AGENT_KEY: 'k' })
  const p = read(h, 'models.json').providers.javo
  assert.equal(p.baseUrl, 'https://openrouter.ai/api/v1')
  assert.equal(p.compat, undefined)
})

test('what pi writes on its own first start (an empty auth.json, its changelog mark) does not count as the person configuring it', () => {
  const h = home()
  configure(platform(h))
  writeFileSync(join(h, '.pi/agent', 'auth.json'), '{}')
  const s = read(h, 'settings.json')
  writeFileSync(join(h, '.pi/agent/settings.json'), JSON.stringify({ ...s, lastChangelogVersion: '1.4.0' }))
  configure({ ...platform(h), JAVO_AGENT_MODEL: 'other/model' })
  assert.deepEqual(read(h, 'settings.json'), { defaultProvider: 'javo', defaultModel: 'other/model', lastChangelogVersion: '1.4.0' })
  assert.deepEqual(read(h, 'models.json').providers.javo.models.map((m) => m.id), ['other/model'])
})

test('the first prompt runs on the first start of the app only', () => {
  const h = home()
  const env = { HOME: h, HOSTNAME: 'pi-app-1', JAVO_AGENT_PROMPT: 'build a dashboard' }
  assert.equal(firstStart(env), true)
  assert.equal(firstStart(env), false)
  // Another app, or another prompt, is a first start of its own.
  assert.equal(firstStart({ ...env, HOSTNAME: 'pi-app-2' }), true)
  assert.equal(firstStart({ ...env, JAVO_AGENT_PROMPT: 'something else' }), true)
  assert.equal(firstStart({ HOME: h, HOSTNAME: 'pi-app-3' }), false)
})
