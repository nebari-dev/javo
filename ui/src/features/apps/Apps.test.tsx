import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { allReady, allStopped, me, NOTICES, stepsAt, storageView, templates, withSince, ws } from '../../test/fixtures'

afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

const running = ws('research-lab', 'Research lab', 'Running', allReady(), { kind: 'jupyterlab', template: 'jupyterlab', file: '' })
const stopped = ws('sales-app', 'Sales app', 'Stopped', allStopped())
const crashed = ws('churn', 'Churn dashboard', 'Failed', stepsAt(3, 'AppCrashed'))

function withApps(extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces': { body: { items: [running, stopped, crashed] } },
    'GET /api/templates': { body: { items: templates } },
    ...extra,
  })
}

async function listView() {
  await userEvent.click(await screen.findByRole('button', { name: 'List' }))
  return screen.getByRole('table', { name: 'Your apps' })
}

function rowOf(name: string): HTMLElement {
  return screen.getByRole('link', { name }).closest('tr') as HTMLElement
}

describe('Apps', () => {
  it('offers no export or import of apps; those are in the CLI and API', async () => {
    withApps()
    renderApp('/apps')
    await screen.findByRole('link', { name: 'Research lab' })
    expect(screen.queryByRole('link', { name: 'Export' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Import' })).toBeNull()
  })

  it('has a single New app control, the one in the top bar', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces': { body: { items: [] } } })
    renderApp('/apps')
    await screen.findByRole('heading', { name: 'No apps yet' })
    const controls = screen.queryAllByRole('button', { name: 'New app' })
    expect(controls).toHaveLength(1)
    expect(controls[0].closest('header')).toBeTruthy()
  })

  it('shows an empty state that leads to creating the first app', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces': { body: { items: [] } } })
    renderApp('/apps')
    const heading = await screen.findByRole('heading', { name: 'No apps yet' })
    const empty = heading.closest('.empty-state') as HTMLElement
    expect(within(empty).getByText('Start JupyterLab, VS Code or a Streamlit app in a few clicks. Apps you create show up here.')).toBeTruthy()
    expect(within(empty).getByRole('link', { name: 'Create your first app' }).getAttribute('href')).toBe('/apps/new')
  })

  it('shows cards by default and switches to a list that persists', async () => {
    withApps()
    const first = renderApp('/apps')
    const cards = await screen.findByRole('button', { name: 'Cards' })
    expect(cards.getAttribute('aria-pressed')).toBe('true')
    expect(screen.queryByRole('table')).toBeNull()
    await listView()
    expect(screen.getByRole('button', { name: 'List' }).getAttribute('aria-pressed')).toBe('true')
    first.unmount()
    renderApp('/apps')
    expect(await screen.findByRole('table', { name: 'Your apps' })).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Cards' }))
    expect(screen.queryByRole('table')).toBeNull()
  })

  it('lists one row per app with name, type and status, and no address text', async () => {
    withApps()
    renderApp('/apps')
    const table = await listView()
    expect(within(table).getAllByRole('row')).toHaveLength(4)
    const row = rowOf('Research lab')
    expect(within(row).getByRole('link', { name: 'Research lab' }).getAttribute('href')).toBe('/apps/research-lab')
    await waitFor(() => expect(within(row).getByText('JupyterLab')).toBeTruthy())
    expect(within(row).getByText('Running')).toBeTruthy()
    expect(row.textContent).not.toContain('/u/alice/research-lab/')
    expect(within(table).queryByRole('columnheader', { name: 'Link' })).toBeNull()
    expect(within(rowOf('Sales app')).getByText('sales/app.py')).toBeTruthy()
  })

  it('shows how long a running app has been up on its card and row, and nothing for a stopped one', async () => {
    const ready = new Date(Date.now() - (3 * 86400 + 4 * 3600 + 60) * 1000).toISOString()
    const up = { ...running, steps: withSince(allReady(), ready, [0, 0, 0, 0, 0]) }
    withApps({ 'GET /api/workspaces': { body: { items: [up, stopped] } } })
    renderApp('/apps')
    expect(await screen.findByText('3 d 4 h')).toBeTruthy()
    const table = await listView()
    expect(within(rowOf('Research lab')).getByText('3 d 4 h')).toBeTruthy()
    expect(rowOf('Sales app').querySelector('time')).toBeNull()
    expect(within(table).getAllByText(/^\d+ (s|min|h|d)/)).toHaveLength(1)
  })

  it('opens running apps and starts stopped ones from the row', async () => {
    const api = withApps({ 'POST /api/workspaces/sales-app/start': { body: ws('sales-app', 'Sales app', 'Pending', stepsAt(0, 'Starting')) } })
    renderApp('/apps')
    await listView()
    expect(within(rowOf('Research lab')).getByRole('link', { name: 'Open' }).getAttribute('href')).toBe('/u/alice/research-lab/')
    // An app opens in its own tab, leaving this page where it was.
    expect(within(rowOf('Research lab')).getByRole('link', { name: 'Open' }).getAttribute('target')).toBe('_blank')
    expect(within(rowOf('Research lab')).queryByRole('button', { name: 'Start' })).toBeNull()
    await userEvent.click(within(rowOf('Sales app')).getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(api.count('POST', '/api/workspaces/sales-app/start')).toBe(1))
    const failed = rowOf('Churn dashboard')
    expect(within(failed).getByRole('button', { name: 'Stop retrying' })).toBeTruthy()
    expect(within(failed).queryByRole('link', { name: 'Open' })).toBeNull()
  })

  it('shows a failed app reason under its name', async () => {
    withApps()
    renderApp('/apps')
    await listView()
    expect(within(rowOf('Churn dashboard')).getByText('App crashed on start')).toBeTruthy()
  })

  it('falls back to cards when storage throws', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    withApps()
    renderApp('/apps')
    expect((await screen.findByRole('button', { name: 'Cards' })).getAttribute('aria-pressed')).toBe('true')
    expect(await listView()).toBeTruthy()
  })

  it('shows the storage notice above the apps', async () => {
    withApps({ 'GET /api/storage': { body: storageView({ notices: [NOTICES.MigrationScheduled] as never }) } })
    renderApp('/apps')
    expect(await screen.findByText('Your files are moving')).toBeTruthy()
  })
})

describe('Start and stop from the apps list', () => {
  function card(name: string): HTMLElement {
    return screen.getByRole('link', { name }).closest('.acard') as HTMLElement
  }

  it('stops a running app from its card after asking, without opening the app', async () => {
    const api = withApps({ 'POST /api/workspaces/research-lab/stop': { body: ws('research-lab', 'Research lab', 'Stopping', allReady()) } })
    renderApp('/apps')
    await screen.findByRole('link', { name: 'Research lab' })
    await userEvent.click(within(card('Research lab')).getByRole('button', { name: 'Stop' }))
    expect(api.count('POST', '/api/workspaces/research-lab/stop')).toBe(0)
    const dialog = screen.getByRole('alertdialog', { name: 'Stop Research lab?' })
    await userEvent.click(within(dialog).getByRole('button', { name: 'Stop Research lab' }))
    await waitFor(() => expect(api.count('POST', '/api/workspaces/research-lab/stop')).toBe(1))
    expect(location()).toBe('/apps')
  })

  it('starts a stopped app from its card, and a failed one offers Stop retrying', async () => {
    const api = withApps({ 'POST /api/workspaces/sales-app/start': { body: ws('sales-app', 'Sales app', 'Pending', stepsAt(0, 'Starting')) } })
    renderApp('/apps')
    await screen.findByRole('link', { name: 'Sales app' })
    expect(within(card('Churn dashboard')).getByRole('button', { name: 'Stop retrying' })).toBeTruthy()
    await userEvent.click(within(card('Sales app')).getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(api.count('POST', '/api/workspaces/sales-app/start')).toBe(1))
  })

  it('stops a running app from its row in the list view', async () => {
    const api = withApps({ 'POST /api/workspaces/research-lab/stop': { body: ws('research-lab', 'Research lab', 'Stopping', allReady()) } })
    renderApp('/apps')
    await listView()
    await userEvent.click(within(rowOf('Research lab')).getByRole('button', { name: 'Stop' }))
    await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop Research lab' }))
    await waitFor(() => expect(api.count('POST', '/api/workspaces/research-lab/stop')).toBe(1))
  })
})
