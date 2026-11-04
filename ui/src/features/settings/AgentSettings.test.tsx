import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const status = { mode: 'login', template: 'pi', templateName: 'pi coding agent', available: true }

describe('Settings, AI agent', () => {
  it('stores the person’s own model and key, and shows only the end of the key', async () => {
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/secrets': { body: [] },
      'GET /api/ai/agent': { body: status },
      'PUT /api/ai/agent/settings': { body: { ...status, mode: 'own', model: 'vendor/m1', baseURL: 'https://llm.example.com/v1', keyLast4: '9876' } },
    })
    renderApp('/settings/secrets')
    expect(await screen.findByText(/have no model yet/)).toBeTruthy()
    await userEvent.type(screen.getByLabelText('API address'), 'https://llm.example.com/v1')
    await userEvent.type(screen.getByLabelText('Model'), 'vendor/m1')
    await userEvent.type(screen.getByLabelText('API key'), 'sk-own-PLACEHOLDER-9876')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(calls.calls.find((c) => c.path === '/api/ai/agent/settings')?.body).toEqual({
        baseURL: 'https://llm.example.com/v1',
        model: 'vendor/m1',
        apiKey: 'sk-own-PLACEHOLDER-9876',
      }),
    )
    expect(await screen.findByText(/use your own key: vendor\/m1/)).toBeTruthy()
    expect(screen.getByText(/ending in 9876/)).toBeTruthy()
    expect((screen.getByLabelText('API key') as HTMLInputElement).value).toBe('')
  })

  it('says when the admin’s model is in effect, with no form', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/secrets': { body: [] }, 'GET /api/ai/agent': { body: { ...status, mode: 'platform', model: 'admin/model' } } })
    renderApp('/settings/secrets')
    expect(await screen.findByText(/model your admin set up: admin\/model/)).toBeTruthy()
    expect(screen.queryByLabelText('API key')).toBeNull()
  })
})
