import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderIn } from '../../test/api'
import { previewApp, repoApp, staticTemplate, ws, allReady } from '../../test/fixtures'
import { SourceCard } from './SourceCard'
import { choose } from '../../test/select'

afterEach(() => vi.unstubAllGlobals())

const templates = { 'GET /api/templates': { body: { items: [staticTemplate] } } }

describe('SourceCard', () => {
  it('shows the repository, the branch and the short commit linking to its page', async () => {
    mockApi(templates)
    renderIn(<SourceCard ws={repoApp('site')} />)
    expect(screen.getByRole('link', { name: 'github.com/example-org/site' }).getAttribute('href')).toBe('https://github.com/example-org/site')
    expect(screen.getByText('main')).toBeTruthy()
    const link = screen.getByRole('link', { name: '0123456' })
    expect(link.getAttribute('href')).toBe('https://github.com/example-org/site/commit/0123456789abcdef0123456789abcdef01234567')
  })

  it('says the commit is not cloned yet instead of showing an empty one', () => {
    mockApi(templates)
    const app = repoApp('site')
    renderIn(<SourceCard ws={{ ...app, source: { ...app.source!, commit: '', webURL: '', ref: '' } }} />)
    expect(screen.getByText('Not cloned yet')).toBeTruthy()
    expect(screen.getByText('Default branch')).toBeTruthy()
  })

  it('posts one redeploy when Redeploy is pressed', async () => {
    const api = mockApi({ ...templates, 'POST /api/workspaces/site/redeploy': { body: repoApp('site') } })
    renderIn(<SourceCard ws={repoApp('site')} />)
    await userEvent.click(screen.getByRole('button', { name: 'Redeploy' }))
    await waitFor(() => expect(api.count('POST', '/api/workspaces/site/redeploy')).toBe(1))
  })

  it('cannot redeploy while the app is starting', () => {
    mockApi(templates)
    renderIn(<SourceCard ws={repoApp('site', { phase: 'Starting' })} />)
    expect((screen.getByRole('button', { name: 'Redeploy' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('shows Redeploy for a static app from a home file, with no repository', async () => {
    mockApi(templates)
    renderIn(<SourceCard ws={ws('page', 'Page', 'Running', allReady(), { template: 'static-site', kind: 'static', file: 'index.html', redeployable: true })} />)
    expect(screen.getByRole('button', { name: 'Redeploy' })).toBeTruthy()
    expect(screen.queryByText('Repository')).toBeNull()
  })

  it('follows the gateway: no Redeploy for an app it says cannot be redeployed, however the template looks', () => {
    mockApi(templates)
    const { container } = renderIn(
      <SourceCard ws={ws('page', 'Page', 'Running', allReady(), { template: 'static-site', kind: 'static', file: 'index.html', redeployable: false })} />,
    )
    expect(container.textContent).toBe('')
  })

  it('renders nothing for an app with neither a repository nor a built file', () => {
    mockApi(templates)
    const { container } = renderIn(<SourceCard ws={ws('plain', 'Plain', 'Running', allReady())} />)
    expect(container.textContent).toBe('')
  })

  const secrets = { 'GET /api/secrets': { body: [{ name: 'GH_TOKEN', note: '', createdAt: '2026-10-01T09:00:00Z', updatedAt: '2026-10-01T09:00:00Z', apps: [], jobs: [] }] } }

  it('names the secret the token comes from, never a token, and picks another behind a disclosure', async () => {
    const api = mockApi({ ...templates, ...secrets, 'PUT /api/workspaces/site/source/token': { status: 204 }, 'GET /api/workspaces/site': { body: repoApp('site') } })
    const app = repoApp('site')
    renderIn(<SourceCard ws={{ ...app, source: { ...app.source!, hasToken: true, tokenSecret: 'OLD_TOKEN' } }} />)
    expect(screen.getByText('From OLD_TOKEN')).toBeTruthy()
    expect(screen.queryByLabelText('Token from')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Change or remove the access token' }))
    const pick = await screen.findByLabelText('Token from')
    expect(pick.textContent).toBe('OLD_TOKEN')
    await choose(pick, 'GH_TOKEN')
    await userEvent.click(screen.getByRole('button', { name: 'Save token' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ secret: 'GH_TOKEN' }))
  })

  it('removes the token', async () => {
    const api = mockApi({ ...templates, ...secrets, 'DELETE /api/workspaces/site/source/token': { status: 204 }, 'GET /api/workspaces/site': { body: repoApp('site') } })
    const app = repoApp('site')
    renderIn(<SourceCard ws={{ ...app, source: { ...app.source!, hasToken: true, tokenSecret: 'GH_TOKEN' } }} />)
    await userEvent.click(screen.getByRole('button', { name: 'Change or remove the access token' }))
    await userEvent.click(screen.getByRole('button', { name: 'Remove token' }))
    await waitFor(() => expect(api.count('DELETE', '/api/workspaces/site/source/token')).toBe(1))
  })

  it('offers to add a token when there is none, and shows the API text when it is refused', async () => {
    mockApi({
      ...templates,
      ...secrets,
      'PUT /api/workspaces/site/source/token': { status: 403, body: { title: 'Only the owner can change its environment variables, with their own secrets.', status: 403 } },
    })
    renderIn(<SourceCard ws={repoApp('site')} />)
    expect(screen.getByText('Not set')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Add a token for a private repository' }))
    expect(screen.queryByRole('button', { name: 'Remove token' })).toBeNull()
    await choose(screen.getByLabelText('Token from'), 'GH_TOKEN')
    await userEvent.click(screen.getByRole('button', { name: 'Save token' }))
    expect(await screen.findByText(/Only the owner can change/)).toBeTruthy()
  })

  it('says there are no secrets yet and links to a new one', async () => {
    mockApi({ ...templates, 'GET /api/secrets': { body: [] } })
    renderIn(<SourceCard ws={repoApp('site')} />)
    await userEvent.click(screen.getByRole('button', { name: 'Add a token for a private repository' }))
    expect((await screen.findByRole('link', { name: 'add one in Settings' })).getAttribute('href')).toBe('/settings/secrets?secret=new')
  })

  it('on a preview offers Redeploy but no token controls', () => {
    mockApi(templates)
    renderIn(<SourceCard ws={previewApp('site', 12)} />)
    expect(screen.getByRole('button', { name: 'Redeploy' })).toBeTruthy()
    expect(screen.queryByText(/access token/)).toBeNull()
    expect(within(document.body).queryByText('Private repo token')).toBeNull()
  })
})
