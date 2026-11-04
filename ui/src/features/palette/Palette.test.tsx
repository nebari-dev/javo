import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { allReady, containerTemplate, customTemplate, jobTemplate, me, staticTemplate, stepsAt, templateCategories, templates, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const sales = ws('sales', 'Sales app', 'Running', allReady())
const churn = ws('churn', 'Churn dashboard', 'Running', allReady())
const orders = ws('orders-api', 'Orders API', 'Running', allReady(), { kind: 'custom', template: 'custom' })
const dashTemplate = { ...templates[1], name: 'dash', displayName: 'Dash', kind: 'dash', description: 'Plotly dashboards', sample: undefined }
const lab = ws('lab', 'Research lab', 'Starting', stepsAt(2, 'Starting'), { kind: 'jupyterlab', template: 'jupyterlab' })
const forecast = {
  name: 'forecast',
  displayName: 'Sales forecast',
  template: 'panel',
  kind: 'panel',
  phase: 'Running',
  url: 'https://apps.example.com/u/bob/forecast/',
  owner: { id: 'bob-bbbbbbbb', displayName: 'Bob' },
  via: 'user',
}
const report = { name: 'weekly-report', displayName: 'Weekly report', engine: 'papermill', source: 'r.ipynb', template: 'python-jobs', phase: 'Ready', recent: [], nextRuns: [] }

function api(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces': { body: { items: [sales, lab, churn, orders] } },
    'GET /api/shared': { body: { items: [forecast] } },
    'GET /api/templates': { body: { items: [...templates, dashTemplate] } },
    'GET /api/jobs': { body: { items: [report] } },
    'GET /api/info': { body: { apiDocs: true } },
    'GET /api/tokens': { body: [] },
    ...extra,
  })
}

const trigger = () => screen.getByRole('button', { name: /^Search/ })

async function open() {
  await userEvent.keyboard('{Meta>}k{/Meta}')
  return screen.findByRole('dialog', { name: 'Search and commands' })
}

async function search(text: string) {
  const dialog = await open()
  await userEvent.type(within(dialog).getByRole('combobox'), text)
  return dialog
}

describe('Palette', () => {
  it('opens with Cmd+K as a labelled modal dialog with the input focused', async () => {
    api()
    renderApp('/templates')
    const dialog = await open()
    expect(dialog.getAttribute('aria-modal')).toBe('true')
    expect(document.activeElement).toBe(within(dialog).getByRole('combobox', { name: 'Search' }))
    expect(within(dialog).getByRole('listbox', { name: 'Results' })).toBeTruthy()
  })

  it('opens with Ctrl+K and from the search field', async () => {
    api()
    renderApp('/templates')
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(await screen.findByRole('dialog', { name: 'Search and commands' })).toBeTruthy()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await userEvent.click(trigger())
    expect(await screen.findByRole('dialog', { name: 'Search and commands' })).toBeTruthy()
  })

  it('closes on Escape and gives focus back to the search field', async () => {
    api()
    renderApp('/templates')
    await open()
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(document.activeElement).toBe(trigger())
  })

  it('finds the user\'s app by a fuzzy query, with its status, and Enter opens its page', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('sal')
    const option = await within(dialog).findByRole('option', { name: /Sales app/ })
    expect(within(option).getByText('Running')).toBeTruthy()
    expect(within(dialog).getAllByRole('option')[0]).toBe(option)
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(location()).toBe('/apps/sales'))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('lists apps shared with the user with their owner, and Enter opens the app itself in a new tab', async () => {
    api()
    const open = vi.fn()
    vi.stubGlobal('open', open)
    renderApp('/templates')
    const dialog = await search('forecast')
    const option = await within(dialog).findByRole('option', { name: /Sales forecast/ })
    expect(within(option).getByText('from Bob')).toBeTruthy()
    expect(within(option).getByText('Running')).toBeTruthy()
    await userEvent.keyboard('{Enter}')
    expect(open).toHaveBeenCalledWith('https://apps.example.com/u/bob/forecast/', '_blank', 'noopener')
  })

  it('starts New app with the template chosen, without its sample', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('new streamlit')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toContain('New Streamlit app'))
    await userEvent.keyboard('{Enter}')
    expect(await screen.findByRole('button', { name: 'Streamlit, change' })).toBeTruthy()
    expect(await screen.findByText('Choose a file from your home folder')).toBeTruthy()
  })

  it('offers a template\'s sample, which opens New app with the sample chosen', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('try streamlit')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toContain('Try the Streamlit sample'))
    await userEvent.keyboard('{Enter}')
    expect(await screen.findByRole('button', { name: 'Code: Home folder · Sample: Sales explorer, change' })).toBeTruthy()
  })

  it('switches the theme, and the theme switch follows', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('dark')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toContain('Dark theme'))
    await userEvent.keyboard('{Enter}')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(screen.getByRole('button', { name: 'Appearance: Dark' })).toBeTruthy()
    document.documentElement.removeAttribute('data-theme')
  })

  it('goes to a page, and lists API docs only when the server serves them', async () => {
    api({ 'GET /api/info': { body: { apiDocs: false } } })
    renderApp('/templates')
    let dialog = await search('settings')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toBe('Settings'))
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(location()).toBe('/settings'))
    // Settings has asked the server already, so the answer is known.
    dialog = await search('api docs')
    expect(await within(dialog).findByText('No matches for “api docs”')).toBeTruthy()
  })

  it('offers New job and New API token as actions', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('new api token')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toBe('New API token'))
    await userEvent.keyboard('{Enter}')
    expect(await screen.findByRole('heading', { name: 'New token' })).toBeTruthy()
  })

  it('lists an app\'s actions on Tab, and Stop asks before it stops the app', async () => {
    const calls = api({ 'POST /api/workspaces/sales/stop': { body: { ...sales, phase: 'Stopping' } } })
    renderApp('/templates')
    const dialog = await search('sales app')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toContain('Sales app'))
    await userEvent.keyboard('{Tab}')
    const actions = await within(dialog).findByRole('group', { name: 'Sales app' })
    expect(within(actions).getAllByRole('option').map((o) => o.textContent)).toEqual(['Open in new tab', 'Stop'])
    await userEvent.type(within(dialog).getByRole('combobox'), 'stop{Enter}')
    const confirm = await screen.findByRole('alertdialog', { name: 'Stop Sales app?' })
    expect(calls.count('POST', '/api/workspaces/sales/stop')).toBe(0)
    await userEvent.click(within(confirm).getByRole('button', { name: 'Stop' }))
    await waitFor(() => expect(calls.count('POST', '/api/workspaces/sales/stop')).toBe(1))
  })

  it('goes back from the actions list with Backspace on an empty field, and Escape too', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('sales app')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toContain('Sales app'))
    await userEvent.keyboard('{ArrowRight}')
    expect(await within(dialog).findByRole('group', { name: 'Sales app' })).toBeTruthy()
    await userEvent.keyboard('{Backspace}')
    expect(within(dialog).queryByRole('group', { name: 'Sales app' })).toBeNull()
    await userEvent.type(within(dialog).getByRole('combobox'), 'sales app{Tab}')
    expect(await within(dialog).findByRole('group', { name: 'Sales app' })).toBeTruthy()
    await userEvent.keyboard('{Escape}')
    expect(within(dialog).queryByRole('group', { name: 'Sales app' })).toBeNull()
    expect(screen.getByRole('dialog')).toBeTruthy()
  })

  it('finds the user\'s jobs; Enter opens one and Run now runs it', async () => {
    const calls = api({ 'POST /api/jobs/weekly-report/run': { status: 201, body: { id: 'run-1', outcome: 'Running' } } })
    renderApp('/templates')
    let dialog = await search('weekly')
    const jobs = await within(dialog).findByRole('group', { name: 'Jobs' })
    expect(within(jobs).getByRole('option', { name: /Weekly report/ })).toBeTruthy()
    await userEvent.keyboard('{Tab}')
    await userEvent.type(within(dialog).getByRole('combobox'), 'run{Enter}')
    await waitFor(() => expect(calls.count('POST', '/api/jobs/weekly-report/run')).toBe(1))
    await waitFor(() => expect(location()).toBe('/jobs/weekly-report'))

    dialog = await search('new job')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toBe('New job'))
  })

  it('shows suggestions with nothing typed, and recent items once used, newest first', async () => {
    api()
    const view = renderApp('/templates')
    let dialog = await open()
    const suggested = await within(dialog).findByRole('group', { name: 'Suggestions' })
    expect(within(suggested).getByRole('option', { name: 'New app' })).toBeTruthy()
    expect(within(dialog).queryByRole('group', { name: 'Recent' })).toBeNull()
    await userEvent.keyboard('{Escape}')

    dialog = await search('sales app')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toContain('Sales app'))
    await userEvent.keyboard('{Enter}')
    dialog = await search('settings')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toBe('Settings'))
    await userEvent.keyboard('{Enter}')
    view.unmount()

    renderApp('/templates')
    dialog = await open()
    const recent = await within(dialog).findByRole('group', { name: 'Recent' })
    await waitFor(() => expect(within(recent).getAllByRole('option').map((o) => o.textContent)).toEqual(['Settings', expect.stringContaining('Sales app')]))
  })

  it('suggests creating an app from the first template of each category, which opens New app with it', async () => {
    api({ 'GET /api/templates': { body: { items: [...templates, containerTemplate, customTemplate, staticTemplate, jobTemplate], categories: templateCategories } } })
    renderApp('/templates')
    const dialog = await open()
    const create = await within(dialog).findByRole('group', { name: 'Create an app' })
    await waitFor(() =>
      expect(within(create).getAllByRole('option').map((o) => o.querySelector('.pal-title')?.textContent)).toEqual([
        'Create JupyterLab app',
        'Create Streamlit app',
        'Create Static site app',
      ]),
    )
    expect(within(dialog).getByRole('group', { name: 'Suggestions' })).toBeTruthy()
    await userEvent.click(within(create).getByRole('option', { name: /Create Streamlit app/ }))
    await waitFor(() => expect(location()).toBe('/apps/new?template=streamlit'))
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('works without storage', async () => {
    api()
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    renderApp('/templates')
    const dialog = await search('settings')
    await waitFor(() => expect(within(dialog).getAllByRole('option')[0].textContent).toBe('Settings'))
    await userEvent.keyboard('{Enter}')
    await waitFor(() => expect(location()).toBe('/settings'))
    vi.restoreAllMocks()
  })

  const names = (group: HTMLElement) => within(group).getAllByRole('option').map((o) => o.querySelector('.pal-title')?.textContent)

  it('matches words and substrings, not scattered letters', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('sa')
    const apps = await within(dialog).findByRole('group', { name: 'Apps' })
    await waitFor(() => expect(names(apps)).toEqual(['Sales app', 'Sales forecast']))
    // Words starting with "sa" first; then initials (Streamlit app).
    const tpl = within(dialog).getByRole('group', { name: 'Templates' })
    expect(names(tpl)).toEqual(['Try the Streamlit sample', 'Try the JupyterLab sample', 'New Streamlit app'])
    expect(within(dialog).queryByRole('group', { name: 'Jobs' })).toBeNull()
  })

  it('matches the first letters of words, and marks the matched letters', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('sf')
    await waitFor(() => expect(within(dialog).getAllByRole('option')).toHaveLength(1))
    const option = within(dialog).getByRole('option', { name: /Sales forecast/ })
    expect([...option.querySelectorAll('mark')].map((m) => m.textContent)).toEqual(['S', 'f'])
  })

  it('finds an app and a template by a word inside their names', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('dash')
    const apps = await within(dialog).findByRole('group', { name: 'Apps' })
    await waitFor(() => expect(names(apps)).toEqual(['Churn dashboard']))
    expect(names(within(dialog).getByRole('group', { name: 'Templates' }))).toEqual(['New Dash app'])
  })

  it('ignores case and accents', async () => {
    api({ 'GET /api/shared': { body: { items: [{ ...forecast, displayName: 'Café sales' }] } } })
    renderApp('/templates')
    const dialog = await search('CAFE')
    expect(await within(dialog).findByRole('option', { name: /Café sales/ })).toBeTruthy()
  })

  it('shows the empty state when nothing matches', async () => {
    api()
    renderApp('/templates')
    const dialog = await search('xyz')
    expect(await within(dialog).findByText('No matches for “xyz”')).toBeTruthy()
    expect(within(dialog).queryAllByRole('option')).toHaveLength(0)
  })
})
