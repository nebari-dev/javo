import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'

afterEach(() => vi.unstubAllGlobals())

const ME = { body: { id: 'alice-1', name: 'alice', displayName: 'Alice', roles: [], groups: [] } }

function app(name: string, template: string, phase: string) {
  return { name, displayName: name, template, kind: template, phase, url: `/u/alice-1/${name}/`, steps: [], size: 'small' }
}

/** Answers the question the page asks before it starts anything. */
async function startIt(title: string) {
  const dialog = await screen.findByRole('alertdialog', { name: title })
  await userEvent.click(within(dialog).getByRole('button', { name: 'Start' }))
}

describe('the Terminal entry', () => {
  it('asks before it starts a terminal, and starts nothing when cancelled', async () => {
    const api = mockApi({
      'GET /api/me': ME,
      'GET /api/workspaces': { body: { items: [app('shell', 'terminal', 'Stopped')] } },
    })
    renderApp('/terminal')
    const dialog = await screen.findByRole('alertdialog', { name: 'Start your terminal?' })
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(location()).toBe('/apps/shell'))
    expect(api.calls.filter((c) => c.method === 'POST')).toHaveLength(0)
  })

  it('is in the sidebar', async () => {
    mockApi({ 'GET /api/me': ME, 'GET /api/workspaces': { body: { items: [] } } })
    renderApp('/files')
    expect(await screen.findByRole('link', { name: 'Terminal' })).toBeTruthy()
  })

  it('makes a terminal in the home folder the first time, then shows it', async () => {
    const api = mockApi({
      'GET /api/me': ME,
      'GET /api/workspaces': { body: { items: [app('notes', 'jupyterlab', 'Running')] } },
      'POST /api/workspaces': { status: 201, body: app('terminal', 'terminal', 'Starting') },
    })
    renderApp('/terminal')
    await startIt('Start a terminal?')
    await waitFor(() => expect(location()).toBe('/apps/terminal'))
    const made = api.calls.filter((c) => c.method === 'POST' && c.path === '/api/workspaces')
    expect(made).toHaveLength(1)
    expect(made[0].body).toMatchObject({ template: 'terminal', displayName: 'Terminal' })
    expect((made[0].body as { sample?: string }).sample).toBeUndefined()
  })

  it('starts a stopped terminal instead of making another', async () => {
    const api = mockApi({
      'GET /api/me': ME,
      'GET /api/workspaces': { body: { items: [app('shell', 'terminal', 'Stopped')] } },
      'POST /api/workspaces/shell/start': { body: app('shell', 'terminal', 'Starting') },
    })
    renderApp('/terminal')
    await startIt('Start your terminal?')
    await waitFor(() => expect(location()).toBe('/apps/shell'))
    expect(api.count('POST', '/api/workspaces/shell/start')).toBe(1)
    expect(api.count('POST', '/api/workspaces')).toBe(0)
  })

  it('goes straight to a running terminal', async () => {
    const api = mockApi({
      'GET /api/me': ME,
      'GET /api/workspaces': { body: { items: [app('shell', 'terminal', 'Running')] } },
    })
    renderApp('/terminal')
    await waitFor(() => expect(location()).toBe('/apps/shell'))
    expect(api.calls.filter((c) => c.method === 'POST')).toHaveLength(0)
  })
})
