import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../../test/api'
import { adminMe, me } from '../../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

describe('Agents settings', () => {
  it('turns off letting agents act without asking, which ends it where it is on', async () => {
    const api = mockApi({
      'GET /api/me': { body: adminMe },
      'GET /api/admin/agents': { body: { allowTrusted: true, configAllows: true } },
      'PUT /api/admin/agents': (c) => ({ body: { allowTrusted: (c.body as { allowTrusted: boolean }).allowTrusted, configAllows: true } }),
    })
    renderApp('/settings/agents')
    const toggle = await screen.findByRole('switch', { name: 'Owners may let their agents act without asking' })
    expect(toggle.getAttribute('aria-checked')).toBe('true')
    await userEvent.click(toggle)
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ allowTrusted: false }))
    await waitFor(() => expect(toggle.getAttribute('aria-checked')).toBe('false'))
  })

  it('says when the platform config turns it off', async () => {
    mockApi({ 'GET /api/me': { body: adminMe }, 'GET /api/admin/agents': { body: { allowTrusted: true, configAllows: false } } })
    renderApp('/settings/agents')
    expect(await screen.findByText(/agents.allowTrusted is false in the platform config/)).toBeTruthy()
    expect(screen.queryByRole('switch')).toBeNull()
  })

  it('is for platform admins only', async () => {
    mockApi({ 'GET /api/me': { body: me } })
    renderApp('/settings')
    await screen.findByRole('navigation', { name: 'Settings sections' })
    expect(screen.queryByRole('link', { name: 'Agents' })).toBeNull()
  })
})
