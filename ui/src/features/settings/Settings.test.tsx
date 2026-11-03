import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CreatedToken, TokenView } from '../../api'
import { mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

const laptop: TokenView = {
  id: 't-1',
  name: 'laptop',
  scope: 'read',
  prefix: 'javo_a1b2c3d4_',
  createdAt: '2026-10-01T09:00:00Z',
}
const ci: TokenView = {
  id: 't-2',
  name: 'ci',
  scope: 'read-write',
  prefix: 'javo_e5f6a7b8_',
  createdAt: '2026-10-02T09:00:00Z',
  lastUsedAt: '2026-10-30T09:00:00Z',
  expiresAt: '2026-12-01T09:00:00Z',
}
const agent: TokenView = {
  id: 't-3',
  name: 'agent app',
  scope: 'read-write',
  prefix: 'javo_c9d0e1f2_',
  createdAt: '2026-10-03T09:00:00Z',
  app: 'agent',
}
const SECRET = 'javo_0f1e2d3c_s3cr3tv4lu3s3cr3tv4lu3s3cr3t'
const created: CreatedToken = {
  id: 't-4',
  name: 'notebook',
  scope: 'read',
  prefix: 'javo_0f1e2d3c_',
  createdAt: '2026-10-31T09:00:00Z',
  expiresAt: '2026-11-30T09:00:00Z',
  token: SECRET,
}
const createdView: TokenView = { id: created.id, name: created.name, scope: created.scope, prefix: created.prefix, createdAt: created.createdAt, expiresAt: created.expiresAt }

function setup(routes: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/info': { body: { apiDocs: false } },
    'GET /api/tokens': { body: [laptop, ci, agent] },
    ...routes,
  })
}

const rows = async () => within(await screen.findByRole('list', { name: 'API tokens' })).getAllByRole('listitem')
const row = async (name: string) => (await rows()).find((r) => within(r).queryByRole('heading', { name }))!

describe('Settings', () => {
  const scroll = Element.prototype.scrollIntoView
  afterEach(() => {
    Element.prototype.scrollIntoView = scroll
  })

  // Chrome's scrollIntoView now returns a promise; an effect that returns it
  // makes React call it as a cleanup and blanks the page on the next tab.
  it('keeps the page when switching tabs in a browser whose scrollIntoView returns a promise', async () => {
    setup({ 'GET /api/secrets': { body: [] } })
    Element.prototype.scrollIntoView = vi.fn(() => Promise.resolve()) as never
    renderApp('/settings')
    await screen.findByRole('heading', { level: 1, name: 'Settings' })
    await userEvent.click(screen.getByRole('link', { name: 'Secrets' }))
    await userEvent.click(screen.getByRole('link', { name: 'API tokens' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy()
  })

  it('lists tokens with their scope, prefix and dates', async () => {
    setup()
    renderApp('/settings')
    expect(await screen.findByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy()
    expect(screen.getByText(/Authorization: Bearer <token>/)).toBeTruthy()
    const l = await row('laptop')
    expect(within(l).getByText('Read')).toBeTruthy()
    expect(within(l).getByText('javo_a1b2c3d4_')).toBeTruthy()
    expect(within(l).getByText('Last used').nextSibling?.textContent).toBe('Never')
    expect(within(l).getByText('Expires').nextSibling?.textContent).toBe('Never')
    const c = await row('ci')
    expect(within(c).getByText('Read and write')).toBeTruthy()
    expect(within(c).getByText('Expires').nextSibling?.textContent).not.toBe('Never')
  })

  it('marks a token made for an app', async () => {
    setup()
    renderApp('/settings')
    expect(within(await row('agent app')).getByText('Used by app agent')).toBeTruthy()
    expect(within(await row('laptop')).queryByText(/Used by app/)).toBeNull()
  })

  it('shows an empty state when there are no tokens', async () => {
    setup({ 'GET /api/tokens': { body: [] } })
    renderApp('/settings')
    expect(await screen.findByText('No API tokens yet')).toBeTruthy()
  })

  it("shows the server's title when the list is refused", async () => {
    setup({ 'GET /api/tokens': { status: 403, body: { title: 'Sign in again to manage API tokens' } } })
    renderApp('/settings')
    expect((await screen.findByRole('alert')).textContent).toContain('Sign in again to manage API tokens')
  })

  it('creates a token, shows it once, and afterwards only its prefix', async () => {
    const api = setup({
      'GET /api/tokens': [{ body: [laptop] }, { body: [laptop, createdView] }],
      'POST /api/tokens': { status: 201, body: created },
    })
    renderApp('/settings')
    await row('laptop')
    await userEvent.click(screen.getByRole('button', { name: 'New token' }))
    await userEvent.type(screen.getByLabelText('Name'), 'notebook')
    expect(screen.getByRole('radio', { name: 'Read' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('radio', { name: '30 days' }).getAttribute('aria-checked')).toBe('true')
    await userEvent.click(screen.getByRole('button', { name: 'Create token' }))
    expect(await screen.findByText(SECRET)).toBeTruthy()
    expect(screen.getByText(/You will not see this token again\./)).toBeTruthy()
    expect(api.calls.find((c) => c.method === 'POST')?.body).toEqual({ name: 'notebook', scope: 'read', expiresInDays: 30 })

    await userEvent.click(screen.getByRole('button', { name: 'Done' }))
    expect(screen.queryByText(SECRET)).toBeNull()
    expect(within(await row('notebook')).getByText('javo_0f1e2d3c_')).toBeTruthy()
    expect(document.body.textContent).not.toContain(SECRET)
  })

  it('copies the new token', async () => {
    setup({ 'POST /api/tokens': { status: 201, body: created } })
    const user = userEvent.setup()
    renderApp('/settings')
    await user.click(await screen.findByRole('button', { name: 'New token' }))
    await user.type(screen.getByLabelText('Name'), 'notebook')
    await user.click(screen.getByRole('button', { name: 'Create token' }))
    await user.click(await screen.findByRole('button', { name: 'Copy' }))
    expect(await navigator.clipboard.readText()).toBe(SECRET)
    expect(screen.getByRole('button', { name: 'Copied' })).toBeTruthy()
  })

  it('sends read and write with a chosen expiry, or none for Never', async () => {
    const api = setup({ 'POST /api/tokens': { status: 201, body: created } })
    renderApp('/settings')
    await userEvent.click(await screen.findByRole('button', { name: 'New token' }))
    await userEvent.type(screen.getByLabelText('Name'), 'deploy')
    await userEvent.click(screen.getByRole('radio', { name: 'Read and write' }))
    expect(screen.queryByRole('radio', { name: 'Never' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Create a token that never expires' }))
    await userEvent.click(screen.getByRole('radio', { name: 'Never' }))
    await userEvent.click(screen.getByRole('button', { name: 'Create token' }))
    await screen.findByText(SECRET)
    expect(api.calls.find((c) => c.method === 'POST')?.body).toEqual({ name: 'deploy', scope: 'read-write' })
  })

  it('shows field errors from the server under their fields', async () => {
    setup({
      'POST /api/tokens': {
        status: 422,
        body: {
          title: 'Unprocessable Entity',
          errors: [
            { location: 'body.name', message: 'You already have a token called "laptop".' },
            { location: 'body.expiresInDays', message: 'Tokens here must expire within 90 days.' },
          ],
        },
      },
    })
    renderApp('/settings')
    await userEvent.click(await screen.findByRole('button', { name: 'New token' }))
    await userEvent.type(screen.getByLabelText('Name'), 'laptop')
    await userEvent.click(screen.getByRole('button', { name: 'Create token' }))
    expect(await screen.findByText('You already have a token called "laptop".')).toBeTruthy()
    expect(screen.getByLabelText('Name').getAttribute('aria-invalid')).toBe('true')
    const expiry = screen.getByRole('group', { name: 'Expires' })
    expect(within(expiry).getByText('Tokens here must expire within 90 days.')).toBeTruthy()
  })

  it('revokes a token after confirming', async () => {
    const api = setup({
      'GET /api/tokens': [{ body: [laptop, ci] }, { body: [ci] }],
      'DELETE /api/tokens/t-1': { status: 204 },
    })
    renderApp('/settings')
    await userEvent.click(within(await row('laptop')).getByRole('button', { name: 'Revoke laptop' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Revoke laptop?' })
    expect(dialog.textContent).toContain('stops working at once')
    expect(api.count('DELETE', '/api/tokens/t-1')).toBe(0)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Revoke' }))
    expect(api.count('DELETE', '/api/tokens/t-1')).toBe(1)
    await screen.findByRole('list', { name: 'API tokens' })
    expect((await rows()).length).toBe(1)
    expect(screen.queryByRole('heading', { name: 'laptop' })).toBeNull()
  })

  it('tells that an app gets a new token when it next starts', async () => {
    setup()
    renderApp('/settings')
    await userEvent.click(within(await row('agent app')).getByRole('button', { name: 'Revoke agent app' }))
    expect(screen.getByRole('alertdialog').textContent).toContain('agent gets a new token when it next starts')
  })

  it('links the API docs only when the server serves them', async () => {
    setup()
    renderApp('/settings')
    await row('laptop')
    expect(screen.queryByRole('link', { name: /API docs/ })).toBeNull()
  })

  it('links the API docs when they are on', async () => {
    setup({ 'GET /api/info': { body: { apiDocs: true } } })
    renderApp('/settings')
    const link = await screen.findByRole('link', { name: /API docs/ })
    expect(link.getAttribute('href')).toBe('/api/docs/')
  })
})
