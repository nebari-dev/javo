import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { REASONS } from '../../test/fixtures'
import { applyBrand } from '../../lib/brand'

afterEach(() => vi.unstubAllGlobals())

const local = { id: 'local', label: 'Username and password', kind: 'password' }
const keycloak = { id: 'oidc', label: 'Sign in with Keycloak', kind: 'redirect', startURL: '/auth/oidc/start' }
const options = { 'GET /api/auth/options': { body: [local] } }

// The API answers with the catalog text for the code in the query.
const reasonReply = (call: { path: string }) => {
  const code = new URL(call.path, 'http://x').searchParams.get('code') as 'IdpUnreachable' | 'SignInLinkExpired'
  const { title, what, todo } = REASONS[code]
  return { body: { title, what, todo } }
}

const me = { id: 'u-1', username: 'alice', groups: [], roles: ['member'] }

async function signIn(user = 'alice', pass = 'pw-1') {
  await screen.findByLabelText('Username')
  await userEvent.type(screen.getByLabelText('Username'), user)
  await userEvent.type(screen.getByLabelText('Password'), pass)
  await userEvent.click(screen.getByRole('button', { name: 'Sign in' }))
}

describe('Login', () => {
  it('shows the configured name and logo above the form', async () => {
    applyBrand({ name: 'Acme Lab', logoLight: '/branding/logo-light?v=1' })
    mockApi(options)
    renderApp('/login')
    await screen.findByLabelText('Username')
    const card = document.querySelector('.login-card .brand') as HTMLElement
    expect(card.textContent).toBe('Acme Lab')
    expect(card.querySelector('img')?.getAttribute('src')).toBe('/branding/logo-light?v=1')
  })

  it('shows the API message on a wrong password and stays on the page', async () => {
    mockApi({ ...options, 'POST /api/auth/local/login': { status: 401, body: { title: 'Wrong username or password', status: 401 } } })
    renderApp('/login')
    await signIn()
    expect((await screen.findByRole('alert')).textContent).toContain('Wrong username or password')
    expect(location()).toBe('/login')
  })

  it('sends the credentials and goes to the page in next', async () => {
    const api = mockApi({
      ...options,
      'POST /api/auth/local/login': { body: me },
      'GET /api/workspaces/sales-app': { status: 404, body: { title: 'Not Found', detail: 'No app with that name.' } },
    })
    renderApp('/login?next=%2Fapps%2Fsales-app')
    await signIn()
    await waitFor(() => expect(location()).toBe('/apps/sales-app'))
    expect(api.calls.find((c) => c.method === 'POST')?.body).toEqual({ username: 'alice', password: 'pw-1' })
  })

  it('leaves the app for a next the gateway serves, such as a ticket for an app on another host', async () => {
    mockApi({ ...options, 'POST /api/auth/local/login': { body: me } })
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    const ticket = '/api/auth/app-ticket?to=' + encodeURIComponent('https://apps.example.com/u/alice/sales-app/')
    renderApp('/login?next=' + encodeURIComponent(ticket))
    await signIn()
    await waitFor(() => expect(assign).toHaveBeenCalledWith(ticket))
    expect(location()).toBe('/login?next=' + encodeURIComponent(ticket))
  })

  it('ignores a next that points to another site', async () => {
    mockApi({ ...options, 'POST /api/auth/local/login': { body: me }, 'GET /api/workspaces': { body: { items: [] } }, 'GET /api/me': { body: me } })
    renderApp('/login?next=%2F%2Fevil.example.com%2Fx')
    await signIn()
    await waitFor(() => expect(location()).toBe('/'))
  })

  it('lists the providers from the options and keeps the local form', async () => {
    mockApi({ 'GET /api/auth/options': { body: [keycloak, local] } })
    renderApp('/login')
    const link = (await screen.findByRole('link', { name: 'Sign in with Keycloak' })) as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('/auth/oidc/start')
    expect(screen.getByLabelText('Username')).toBeTruthy()
  })

  it('passes next to the provider so sign-in returns to the page', async () => {
    mockApi({ 'GET /api/auth/options': { body: [keycloak, local] } })
    renderApp('/login?next=%2Fapps%2Fsales-app')
    const link = (await screen.findByRole('link', { name: 'Sign in with Keycloak' })) as HTMLAnchorElement
    expect(link.getAttribute('href')).toBe('/auth/oidc/start?next=%2Fapps%2Fsales-app')
  })

  it('goes straight to the only provider, keeping the page to return to', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    mockApi({ 'GET /api/auth/options': { body: [keycloak] } })
    renderApp('/login?next=%2Fapps')
    await waitFor(() => expect(assign).toHaveBeenCalledWith('/auth/oidc/start?next=%2Fapps'))
    expect(screen.queryByLabelText('Username')).toBeNull()
  })

  it('waits for a click after signing out, so the provider does not sign the person straight back in', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    mockApi({ 'GET /api/auth/options': { body: [keycloak] } })
    renderApp('/login?signedOut=1')
    await screen.findByRole('link', { name: 'Sign in with Keycloak' })
    expect(screen.getByText('You signed out.')).toBeTruthy()
    expect(assign).not.toHaveBeenCalled()
  })

  it('does not go to the provider on its own while a sign-in problem is shown', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    mockApi({ 'GET /api/auth/options': { body: [keycloak] }, 'GET /api/reasons': reasonReply })
    renderApp('/login?error=SignInLinkExpired')
    await screen.findByRole('alert')
    expect(assign).not.toHaveBeenCalled()
  })

  it('does not go to the provider on its own when a password form is offered too', async () => {
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    mockApi({ 'GET /api/auth/options': { body: [keycloak, local] } })
    renderApp('/login')
    await screen.findByLabelText('Username')
    expect(assign).not.toHaveBeenCalled()
  })

  it('explains a provider that is down and keeps local sign-in usable', async () => {
    const down = { ...keycloak, disabled: true, reason: 'IdpUnreachable' }
    mockApi({ 'GET /api/auth/options': { body: [down, local] }, 'GET /api/reasons': reasonReply })
    renderApp('/login')
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain(REASONS.IdpUnreachable.title)
    expect(alert.textContent).toContain(REASONS.IdpUnreachable.todo)
    expect(screen.queryByRole('link', { name: 'Sign in with Keycloak' })).toBeNull()
    expect((screen.getByLabelText('Username') as HTMLInputElement).disabled).toBe(false)
  })

  it('shows the expired-link copy and starts again through the provider', async () => {
    mockApi({ 'GET /api/auth/options': { body: [keycloak, local] }, 'GET /api/reasons': reasonReply })
    renderApp('/login?error=SignInLinkExpired')
    const alert = await screen.findByRole('alert')
    expect(alert.textContent).toContain(REASONS.SignInLinkExpired.title)
    expect(alert.textContent).toContain(REASONS.SignInLinkExpired.what)
    const start = await screen.findByRole('button', { name: 'Start again' })
    const assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, origin: window.location.origin, assign })
    await userEvent.click(start)
    expect(assign).toHaveBeenCalledWith('/auth/oidc/start')
  })
})
