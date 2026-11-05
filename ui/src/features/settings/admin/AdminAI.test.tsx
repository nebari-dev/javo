import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp, type Call } from '../../../test/api'
import { adminMe, me } from '../../../test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

const fromConfig = {
  enabled: true,
  overridden: false,
  provider: 'anthropic',
  baseURL: '',
  model: 'claude-opus-5-5',
  keyStored: false,
  keyFrom: 'config',
  config: { provider: 'anthropic', baseURL: '', model: 'claude-opus-5-5', key: true },
}

const saved = {
  ...fromConfig,
  overridden: true,
  provider: 'openai',
  baseURL: 'https://router.example.com/api/v1',
  model: 'vendor/model-a',
  keyStored: true,
  keyLast4: '4321',
  keyFrom: 'settings',
  updatedAt: '2026-11-02T10:00:00Z',
}

async function pick(field: string, option: string) {
  await userEvent.click(screen.getByRole('combobox', { name: field }))
  await userEvent.click(screen.getByRole('option', { name: option }))
}

function api(view: object, put: (c: Call) => object = () => saved) {
  return mockApi({
    'GET /api/me': { body: adminMe },
    'GET /api/admin/ai': { body: view },
    'PUT /api/admin/ai': (c) => ({ body: put(c) }),
    'DELETE /api/admin/ai': { status: 204 },
  })
}

describe('AI settings', () => {
  it('fills the address and model from a provider, then shows only the key end', async () => {
    const m = api(fromConfig)
    renderApp('/settings/ai')
    expect(await screen.findByText(/From the platform config/)).toBeTruthy()
    await pick('Provider', 'OpenRouter')
    expect((screen.getByLabelText('API address') as HTMLInputElement).value).toBe('https://openrouter.ai/api/v1')
    await pick('Model', 'anthropic/claude-sonnet-5.5')
    await userEvent.type(screen.getByLabelText('API key'), 'sk-test-4321')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(/ending in 4321/)).toBeTruthy()
    const put = m.calls.find((c) => c.method === 'PUT')
    expect(put?.body).toEqual({ baseURL: 'https://openrouter.ai/api/v1', model: 'anthropic/claude-sonnet-5.5', apiKey: 'sk-test-4321' })
    expect((screen.getByLabelText('API key') as HTMLInputElement).value).toBe('')
    expect(document.body.textContent).not.toContain('sk-test-4321')
  })

  it('takes any address and model id with Custom', async () => {
    const m = api(fromConfig)
    renderApp('/settings/ai')
    await screen.findByText(/From the platform config/)
    await pick('Provider', 'Custom')
    const url = screen.getByLabelText('API address')
    await userEvent.clear(url)
    await userEvent.type(url, 'https://router.example.com/api/v1')
    const id = screen.getByLabelText('Model id')
    await userEvent.clear(id)
    await userEvent.type(id, 'vendor/model-a')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/ai')).toBe(1))
    expect(m.calls.find((c) => c.method === 'PUT')?.body).toEqual({ baseURL: 'https://router.example.com/api/v1', model: 'vendor/model-a' })
  })

  it("starts an anthropic platform config at Anthropic's OpenAI-compatible address", async () => {
    const m = api(fromConfig)
    renderApp('/settings/ai')
    await screen.findByText(/From the platform config/)
    expect(screen.getByRole('combobox', { name: 'Provider' }).textContent).toContain('Anthropic')
    expect(screen.getByRole('combobox', { name: 'Model' }).textContent).toBe('claude-opus-5-5')
    await pick('Model', 'claude-sonnet-5-5')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/ai')).toBe(1))
    expect(m.calls.find((c) => c.method === 'PUT')?.body).toEqual({ baseURL: 'https://api.anthropic.com/v1/', model: 'claude-sonnet-5-5' })
  })

  it('keeps the stored key when it is left empty, and replaces it when one is typed', async () => {
    const m = api(saved)
    renderApp('/settings/ai')
    expect(await screen.findByText(/ending in 4321/)).toBeTruthy()
    expect((screen.getByLabelText('API address') as HTMLInputElement).value).toBe('https://router.example.com/api/v1')
    const model = screen.getByLabelText('Model id')
    await userEvent.clear(model)
    await userEvent.type(model, 'vendor/model-b')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/ai')).toBe(1))
    expect(m.calls.find((c) => c.method === 'PUT')?.body).toEqual({ baseURL: 'https://router.example.com/api/v1', model: 'vendor/model-b' })

    await userEvent.type(screen.getByLabelText('API key'), 'sk-new-9999')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/ai')).toBe(2))
    expect((m.calls.filter((c) => c.method === 'PUT')[1].body as { apiKey?: string }).apiKey).toBe('sk-new-9999')
  })

  it('removes the stored key', async () => {
    const m = api(saved, () => ({ ...saved, keyStored: false, keyLast4: '', keyFrom: 'none' }))
    renderApp('/settings/ai')
    await userEvent.click(await screen.findByRole('button', { name: 'Remove key' }))
    await vi.waitFor(() => expect(m.count('PUT', '/api/admin/ai')).toBe(1))
    const body = m.calls.find((c) => c.method === 'PUT')?.body as Record<string, unknown>
    expect(body.removeKey).toBe(true)
    expect(body.apiKey).toBeUndefined()
    expect(await screen.findByText(/No key is sent/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Remove key' })).toBeNull()
  })

  it('goes back to the platform config', async () => {
    const m = api(saved)
    renderApp('/settings/ai')
    await userEvent.click(await screen.findByRole('button', { name: 'Use the platform config' }))
    await vi.waitFor(() => expect(m.count('DELETE', '/api/admin/ai')).toBe(1))
  })

  it('shows the field the server refused', async () => {
    mockApi({
      'GET /api/me': { body: adminMe },
      'GET /api/admin/ai': { body: fromConfig },
      'PUT /api/admin/ai': {
        status: 422,
        body: { title: 'Invalid', status: 422, errors: [{ location: 'body.baseURL', message: 'Give the https address of the provider.' }] },
      },
    })
    renderApp('/settings/ai')
    const url = await screen.findByLabelText('API address')
    await userEvent.clear(url)
    await userEvent.type(url, 'http://llm.example.com')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('Give the https address of the provider.')).toBeTruthy()
  })

  it('says there is nothing to set while AI is off', async () => {
    api({ enabled: false, overridden: false, keyStored: false, config: { provider: '', baseURL: '', model: '', key: false } })
    renderApp('/settings/ai')
    expect(await screen.findByText(/AI is off in the platform config/)).toBeTruthy()
    expect(document.querySelector('input')).toBeNull()
  })

  it('is an admin section of Settings', async () => {
    api(fromConfig)
    renderApp('/settings/ai')
    await screen.findByText(/From the platform config/)
    expect(screen.getByRole('link', { name: 'AI' }).getAttribute('aria-current')).toBe('page')
  })

  it('is not there for members', async () => {
    const m = mockApi({ 'GET /api/me': { body: me }, 'GET /api/secrets': { body: { items: [] } } })
    renderApp('/settings/ai')
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy()
    expect(screen.queryByRole('link', { name: 'AI' })).toBeNull()
    expect(document.querySelector('input')).toBeNull()
    expect(m.count('GET', '/api/admin/ai')).toBe(0)
  })
})
