import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { allReady, me, NOTICES, stepsAt, storageView, templates, ws } from '../../test/fixtures'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const running = ws('research-lab', 'Research lab', 'Running', allReady(), { kind: 'jupyterlab', template: 'jupyterlab', file: '' })
const starting = ws('sales-app', 'Sales app', 'Starting', stepsAt(2, 'Starting'))
const crashed = ws('churn', 'Churn dashboard', 'Failed', stepsAt(3, 'AppCrashed'))
const pulled = ws('model', 'Model trainer', 'Failed', stepsAt(2, 'ImagePullFailed'))
const queued = ws('report', 'Weekly report', 'Starting', stepsAt(0, 'NoCapacity'))
const busy = ws('etl', 'ETL notebook', 'Starting', stepsAt(1, 'HomeBusy'))

describe('Home', () => {
  it('lists the apps with status pills', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces': { body: { items: [running, crashed] } } })
    renderApp('/')
    const card = (await screen.findByText('Research lab')).closest('.acard') as HTMLElement
    expect(within(card).getByText('Running')).toBeTruthy()
    expect(within(card).getByRole('link', { name: 'Research lab' }).getAttribute('href')).toBe('/apps/research-lab')
    const failed = screen.getAllByText('Churn dashboard').map((e) => e.closest('.acard') as HTMLElement)
    expect(failed.some((c) => c && within(c).queryByText('Failed'))).toBe(true)
  })

  it('titles the page Home, with no greeting or email', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces': { body: { items: [] } } })
    renderApp('/')
    const h1 = await screen.findByRole('heading', { level: 1 })
    await screen.findByText('0 apps running')
    expect(h1.textContent).toBe('Home')
  })

  it('shows two items that need attention and the rest behind Show N more', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': { body: { items: [running, crashed, pulled, queued, busy] } },
    })
    renderApp('/')
    const attn = await screen.findByRole('region', { name: 'Needs your attention' })
    // the reason titles come from the API
    expect(within(attn).getByText('App crashed on start')).toBeTruthy()
    expect(within(attn).getByText('Image cannot be downloaded')).toBeTruthy()
    expect(within(attn).queryByText('No machine has room')).toBeNull()
    await userEvent.click(within(attn).getByRole('button', { name: 'Show 2 more items' }))
    expect(within(attn).getByRole('button', { name: 'Hide 2 items' })).toBeTruthy()
    expect(within(attn).getByText('No machine has room')).toBeTruthy()
    expect(within(attn).getByText('Home folder is busy')).toBeTruthy()
    expect(within(attn).queryByText('Research lab')).toBeNull()
  })

  it('has no attention section when nothing needs it', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces': { body: { items: [running, starting] } } })
    renderApp('/')
    await screen.findByText('Research lab')
    expect(screen.queryByRole('region', { name: 'Needs your attention' })).toBeNull()
  })

  it('polls every 2 s while an app is starting and stops once none is', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': [{ body: { items: [starting] } }, { body: { items: [{ ...starting, phase: 'Running', steps: allReady() }] } }],
    })
    renderApp('/')
    await screen.findByText('Starting')
    expect(api.count('GET', '/api/workspaces')).toBe(1)
    await act(() => vi.advanceTimersByTimeAsync(2000))
    await waitFor(() => expect(screen.getByText('Running')).toBeTruthy())
    expect(api.count('GET', '/api/workspaces')).toBe(2)
    await act(() => vi.advanceTimersByTimeAsync(6000))
    expect(api.count('GET', '/api/workspaces')).toBe(2)
  })

  it('sends the user to sign in when the session ends during polling', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': [{ body: { items: [starting] } }, { status: 401, body: { title: 'Sign in required', status: 401 } }],
    })
    renderApp('/')
    await screen.findByText('Sales app')
    await act(() => vi.advanceTimersByTimeAsync(2000))
    await waitFor(() => expect(location()).toBe('/login?next=%2F'))
    expect(screen.getByText('Your session ended. Sign in again.')).toBeTruthy()
  })

  it('sends a first visit without a session to sign in and says the session ended', async () => {
    mockApi({
      'GET /api/me': { status: 401, body: { title: 'Sign in required' } },
      'GET /api/workspaces': { status: 401, body: { title: 'Sign in required' } },
    })
    renderApp('/apps')
    await waitFor(() => expect(location()).toBe('/login?next=%2Fapps'))
    expect(screen.getByText('Your session ended. Sign in again.')).toBeTruthy()
  })

  it('signs out and goes to the sign-in page', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': { body: { items: [running] } },
      'POST /api/auth/logout': { status: 204 },
    })
    renderApp('/')
    await screen.findByText('Research lab')
    await userEvent.click(screen.getByRole('button', { name: /^Account: / }))
    await userEvent.click(screen.getByRole('menuitem', { name: 'Sign out' }))
    await waitFor(() => expect(location()).toBe('/login'))
    expect(api.count('POST', '/api/auth/logout')).toBe(1)
  })

  it('signs out without saying the session ended when logout answers 401', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': { body: { items: [running] } },
      'POST /api/auth/logout': { status: 401, body: { title: 'Sign in required', status: 401 } },
    })
    renderApp('/')
    await screen.findByText('Research lab')
    await userEvent.click(screen.getByRole('button', { name: /^Account: / }))
    await userEvent.click(screen.getByRole('menuitem', { name: 'Sign out' }))
    await waitFor(() => expect(location()).toBe('/login'))
    expect(screen.queryByText('Your session ended. Sign in again.')).toBeNull()
  })

  it('shows the template display name on app cards', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': { body: { items: [running] } },
      'GET /api/templates': { body: { items: templates } },
    })
    renderApp('/')
    const card = (await screen.findByText('Research lab')).closest('.acard') as HTMLElement
    await waitFor(() => expect(within(card).getByText('JupyterLab')).toBeTruthy())
    expect(within(card).queryByText('jupyterlab')).toBeNull()
  })

  it('says how to start when there are no apps', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces': { body: { items: [] } } })
    renderApp('/')
    await screen.findByRole('heading', { name: 'No apps yet' })
    expect(screen.getByText('Start JupyterLab, VS Code or a Streamlit app in a few clicks. Apps you create show up here.')).toBeTruthy()
    const link = screen.getByRole('link', { name: 'Create your first app' })
    expect(link.getAttribute('href')).toBe('/apps/new')
    expect(screen.queryAllByRole('button', { name: 'New app' })).toHaveLength(1)
  })
})

describe('Home samples', () => {
  it('has a Try a sample section with a card per template that has one', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces': { body: { items: [] } }, 'GET /api/templates': { body: { items: templates } } })
    renderApp('/')
    const region = await screen.findByRole('region', { name: 'Try a sample' })
    expect(within(region).getByRole('button', { name: /Streamlit/ })).toBeTruthy()
    expect(within(region).getByRole('button', { name: /JupyterLab/ })).toBeTruthy()
  })

  it('shows its apps in the list view saved on the Apps page', async () => {
    localStorage.setItem('javo.appsView', 'list')
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces': { body: { items: [running] } }, 'GET /api/templates': { body: { items: templates } } })
    renderApp('/')
    const table = await screen.findByRole('table', { name: 'Your apps' })
    expect(within(table).getByRole('link', { name: 'Research lab' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'List' }).getAttribute('aria-pressed')).toBe('true')
  })
})

describe('storage notices', () => {
  const api = (notices: object[]) =>
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': { body: { items: [] } },
      'GET /api/storage': { body: storageView({ notices: notices as never }) },
    })

  it('shows the notice title and text the API sent', async () => {
    api([NOTICES.MigrationScheduled])
    renderApp('/')
    const banner = (await screen.findByText('Your files are moving')).closest('.banner') as HTMLElement
    expect(banner.className).toContain('warn')
    expect(within(banner).getByText(NOTICES.MigrationScheduled.action)).toBeTruthy()
  })

  it('prints a scheduled move date as sent', async () => {
    api([NOTICES.MigrationScheduled])
    renderApp('/')
    expect(await screen.findByText(NOTICES.MigrationScheduled.text)).toBeTruthy()
  })

  it('leaves the scratch notice to the app page', async () => {
    api([NOTICES.ScratchPaths])
    renderApp('/')
    await screen.findByRole('heading', { level: 1 })
    await waitFor(() => expect(screen.queryByText('Some folders are not saved')).toBeNull())
  })
})
