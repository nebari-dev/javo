import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const view = (over: Record<string, unknown> = {}) => ({
  userCode: 'WDJB-MJHT',
  createdAt: new Date().toISOString(),
  expiresAt: new Date(Date.now() + 600_000).toISOString(),
  tokenHours: 8,
  state: 'pending',
  ...over,
})

const ROUTE = 'GET /api/auth/device/WDJB-MJHT'

describe('Device login page', () => {
  it('shows the code, how long access lasts and what an agent can do', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText('WDJB-MJHT')).toBeTruthy()
    expect(screen.getByRole('heading', { name: 'Approve an agent login' })).toBeTruthy()
    expect(screen.getByText('An agent asked to act as you through the API for 8 hours. Approve only if you started this yourself just now.')).toBeTruthy()
    for (const t of [
      'Create, change, start, stop and delete your apps and jobs',
      'Write files in your home folder',
      'Store and change your secrets',
      'Share your apps, including publicly',
      'Give an app its own API access, which lasts until you turn it off',
    ])
      expect(screen.getByText(t)).toBeTruthy()
  })

  it('says "1 hour" for a one hour token', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ tokenHours: 1 }) } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText(/through the API for 1 hour\./)).toBeTruthy()
  })

  it('says "less than an hour" when the token lasts under an hour', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ tokenHours: 0 }) } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText(/through the API for less than an hour\./)).toBeTruthy()
  })

  it('approves and says so', async () => {
    const api = mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() }, 'POST /api/auth/device/WDJB-MJHT/approve': { status: 204 } })
    renderApp('/device?code=WDJB-MJHT')
    await userEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    expect(await screen.findByText('Approved. You can go back to your agent.')).toBeTruthy()
    expect(api.count('POST', '/api/auth/device/WDJB-MJHT/approve')).toBe(1)
    expect(screen.queryByRole('button', { name: 'Deny' })).toBeNull()
  })

  it('denies and says so', async () => {
    const api = mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() }, 'POST /api/auth/device/WDJB-MJHT/deny': { status: 204 } })
    renderApp('/device?code=WDJB-MJHT')
    await userEvent.click(await screen.findByRole('button', { name: 'Deny' }))
    expect(await screen.findByText('Denied. The agent gets no access.')).toBeTruthy()
    expect(api.count('POST', '/api/auth/device/WDJB-MJHT/deny')).toBe(1)
  })

  it('says an expired request has expired', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/auth/device/XXXX-EXPD': { status: 410, body: { title: 'Gone' } } })
    renderApp('/device?code=XXXX-EXPD')
    expect(await screen.findByText('This request has expired. Ask your agent to start again.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
  })

  it('says an unknown code is unknown', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/auth/device/BBBB-CCCC': { status: 404, body: { title: 'Not found' } } })
    renderApp('/device?code=BBBB-CCCC')
    expect(await screen.findByText('No request has this code. Check it and try again.')).toBeTruthy()
  })

  it('asks for the code when none is in the address, and opens it', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() } })
    renderApp('/device')
    await userEvent.type(await screen.findByLabelText('Code shown by your agent'), 'WDJB-MJHT')
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(location()).toBe('/device?code=WDJB-MJHT')
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeTruthy()
  })

  it('returns to the same address after signing in', async () => {
    mockApi({ 'GET /api/me': { status: 401, body: { title: 'Unauthorized' } }, [ROUTE]: { status: 401, body: { title: 'Unauthorized' } } })
    renderApp('/device?code=WDJB-MJHT')
    await waitFor(() => expect(location()).toBe('/login?next=' + encodeURIComponent('/device?code=WDJB-MJHT')))
  })
})
