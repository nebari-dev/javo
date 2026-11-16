import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FoldersView } from '../../api'
import { location, mockApi, renderApp, type Call, type Reply } from '../../test/api'
import { agentTemplate, me, stepsAt, templateCategories, templates, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const HOME: Record<string, string[]> = {
  '': ['data', 'notebooks', 'projects'],
  projects: ['sales-dashboard', 'shared-lib'],
}

function listing(c: Call): Reply {
  const path = new URL(c.path, 'http://x').searchParams.get('path') ?? ''
  const entries = (HOME[path] ?? []).map((name) => ({ name, kind: 'folder', size: 0, modified: '2026-11-02T10:00:00Z' }))
  return { body: { path, entries, count: entries.length, truncated: false, stale: false, ageSeconds: 0 } }
}

const view = (project: string): FoldersView => ({
  project,
  grants: [],
  places: [],
  hidden: { path: '.', rule: 'risky', what: "a tool's settings or sign-ins" },
})

/** GET /api/workspace-folders as the API answers it: agents start locked in, in a folder named after the app unless one is picked. */
function wouldOpen(c: Call): Reply {
  const q = new URL(c.path, 'http://x').searchParams
  const name = q.get('displayName') ?? ''
  if (!/[a-z]/i.test(name)) {
    return { status: 422, body: { title: 'Unprocessable Entity', errors: [{ location: 'query.displayName', message: 'The name needs at least one letter.' }] } }
  }
  if (q.get('template') !== agentTemplate.name) return { body: {} }
  const own = name.toLowerCase().replace(/[^a-z0-9]+/g, '-')
  return { body: { folders: view(q.get('file') ? `~/${q.get('file')}` : `~/projects/${own}`) } }
}

const created = ws('sales-dashboard', 'Sales dashboard', 'Pending', stepsAt(0, 'Starting'))

function base(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items: [agentTemplate, ...templates.map(({ sample: _, ...t }) => t)], categories: templateCategories } },
    'GET /api/files': listing,
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/sizes': { body: { items: [] } },
    'GET /api/workspace-folders': wouldOpen,
    'POST /api/workspaces': { status: 201, body: created },
    'GET /api/workspaces/sales-dashboard': { body: created },
    ...extra,
  })
}

const heading = () => screen.findByRole('heading', { name: 'Which files and folders can Claude Code access?' })
/** Access starts folded to one row; Change opens the file browser. */
async function openAccess() {
  await userEvent.click(await screen.findByRole('button', { name: /^Access: .*, change$/ }))
  return heading()
}
const row = async (name: string) => within(await screen.findByRole('tree', { name: 'Your home' })).findByRole('treeitem', { name })
const tick = (r: HTMLElement) => within(r).getByRole('checkbox') as HTMLInputElement
const posted = (api: ReturnType<typeof mockApi>) => api.calls.find((c) => c.method === 'POST' && c.path === '/api/workspaces')?.body as Record<string, unknown>

async function rename(to: string) {
  const name = screen.getByLabelText('Name')
  await userEvent.clear(name)
  await userEvent.type(name, to)
}

describe('New app, what a locked agent can access', () => {
  it('starts folded to one row naming the project, and Change opens the file browser', async () => {
    base()
    renderApp('/apps/new?template=claude-code')
    await screen.findByRole('button', { name: 'Access: ~/projects/claude-code-app only, change' })
    expect(screen.queryByRole('tree')).toBeNull()
    await openAccess()
    await userEvent.click(tick(await row('data')))
    // The row stays on top of the open card and folds it, counting what was ticked.
    const top = screen.getByRole('button', { name: 'Access: ~/projects/claude-code-app and 1 more, done' })
    expect(top.getAttribute('aria-expanded')).toBe('true')
    await userEvent.click(top)
    expect(screen.queryByRole('tree')).toBeNull()
    expect(top.getAttribute('aria-expanded')).toBe('false')
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Access: ~/projects/claude-code-app and 1 more, change' }))
  })

  it('follows the name of the app, and sends what was ticked with the new app', async () => {
    const api = base()
    renderApp('/apps/new?template=claude-code')
    await openAccess()
    // An agent with no folder gets one named after it, listed where it will be.
    const own = await row('claude-code-app')
    expect(own.textContent).toContain("This app's project, made when the app starts. Claude Code can always read and change it")

    await rename('Sales dashboard')
    const project = await row('sales-dashboard')
    expect(project.textContent).toContain("This app's project. Claude Code can always read and change it")
    await waitFor(() => expect(screen.queryByRole('treeitem', { name: 'claude-code-app' })).toBeNull())

    await userEvent.click(tick(await row('data')))
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/sales-dashboard'))
    expect(posted(api)).toMatchObject({ displayName: 'Sales dashboard', template: 'claude-code', folders: [{ path: 'data', mode: 'read' }] })
  })

  it('sends no folders when nothing was ticked', async () => {
    const api = base()
    renderApp('/apps/new?template=claude-code')
    await screen.findByRole('button', { name: /^Access: / })
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(posted(api)).toBeTruthy())
    expect('folders' in posted(api)).toBe(false)
  })

  it('is not on the form of an app the API says would not start locked in', async () => {
    const api = base()
    renderApp('/apps/new?template=jupyterlab')
    await waitFor(() => expect(api.count('GET', '/api/workspace-folders')).toBeGreaterThan(0))
    await screen.findByRole('button', { name: 'Create app' })
    expect(screen.queryByRole('heading', { name: /can .* access\?/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Access: / })).toBeNull()
    expect(screen.queryByRole('tree')).toBeNull()
  })

  it('stays as it was, without an error, while the name is one no app can have', async () => {
    const api = base()
    renderApp('/apps/new?template=claude-code')
    await openAccess()
    await row('claude-code-app')
    await rename('123')
    await waitFor(() => expect(api.calls.some((c) => c.path.includes('displayName=123'))).toBe(true))
    expect(await row('claude-code-app')).toBeTruthy()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it("shows the server's refusal of a folder under the browser, not as a failure of the whole form", async () => {
    base({
      'POST /api/workspaces': {
        status: 422,
        body: { title: 'Unprocessable Entity', errors: [{ location: 'body.folders', message: 'Not added. "data": it is a link.' }] },
      },
    })
    renderApp('/apps/new?template=claude-code')
    await openAccess()
    await userEvent.click(tick(await row('data')))
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    const card = (await heading()).closest('.card') as HTMLElement
    expect((await within(card).findByRole('alert')).textContent).toBe('Not added. "data": it is a link.')
    expect(screen.queryByText('Nothing was created')).toBeNull()
  })
})
