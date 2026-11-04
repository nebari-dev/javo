import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { aiOff, aiOn, allReady, me, plan, sizes, templates, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const sales = ws('sales', 'Sales app', 'Running', allReady())
const churn = ws('churn', 'Churn dashboard', 'Running', allReady())
const stopSales = plan({ id: 'p1', source: 'palette', planned: { action: 'stop', target: 'sales', summary: 'Stop Sales app', destructive: false, changes: [{ field: 'state', from: 'Running', to: 'Stopped' }] } })
const stopChurn = plan({ id: 'p2', source: 'palette', planned: { action: 'stop', target: 'churn', summary: 'Stop Churn dashboard', destructive: false, changes: [{ field: 'state', from: 'Running', to: 'Stopped' }] } })
const admin = { ...me, roles: ['platform-admin', 'member'] }

function api(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces': { body: { items: [sales, churn] } },
    'GET /api/shared': { body: { items: [] } },
    'GET /api/templates': { body: { items: templates } },
    'GET /api/jobs': { body: { items: [] } },
    'GET /api/info': { body: { apiDocs: false } },
    'GET /api/ai/status': { body: aiOn },
    'POST /api/ai/command': { body: { reply: 'I can stop both idle apps.', plans: [stopSales, stopChurn], dropped: { refs: 0, actions: 0 } } },
    'POST /api/plans/p1/approve': { body: { ...stopSales, state: 'applied' } },
    'POST /api/plans/p2/approve': { body: { ...stopChurn, state: 'applied' } },
    ...extra,
  })
}

async function search(text: string) {
  await userEvent.keyboard('{Meta>}k{/Meta}')
  const dialog = await screen.findByRole('dialog', { name: 'Search and commands' })
  await userEvent.type(within(dialog).getByRole('combobox'), text)
  return dialog
}

describe('Ask AI in the palette', () => {
  it('is the first row for any query, and its answer is plans that change nothing until approved', async () => {
    const calls = api()
    renderApp('/apps')
    const dialog = await search('stop all my idle apps')
    const options = await within(dialog).findAllByRole('option')
    expect(options[0].textContent).toContain('Ask AI')
    await userEvent.keyboard('{Enter}')
    expect(await within(dialog).findByText('I can stop both idle apps.')).toBeTruthy()
    expect(calls.calls.find((c) => c.path === '/api/ai/command')?.body).toEqual({ text: 'stop all my idle apps' })
    expect(within(dialog).getByLabelText('Stop Sales app')).toBeTruthy()
    expect(within(dialog).getByLabelText('Stop Churn dashboard')).toBeTruthy()
    expect(calls.count('POST', '/api/plans/p1/approve') + calls.count('POST', '/api/plans/p2/approve')).toBe(0)
    await userEvent.click(within(within(dialog).getByLabelText('Stop Sales app')).getByRole('button', { name: 'Approve' }))
    expect(calls.count('POST', '/api/plans/p1/approve')).toBe(1)
    expect(calls.count('POST', '/api/plans/p2/approve')).toBe(0)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Approve all' }))
    expect(calls.count('POST', '/api/plans/p2/approve')).toBe(1)
    expect(calls.count('POST', '/api/plans/p1/approve')).toBe(1)
  })

  it('answers a request for a new app with a draft that opens New app filled in, creating nothing', async () => {
    const draft = {
      draft: { displayName: 'Sales dashboard', template: 'streamlit', file: 'sales/app.py' },
      setBy: ['displayName', 'template', 'file'],
      errors: [],
    }
    const calls = api({
      'GET /api/sizes': { body: { items: sizes } },
      'POST /api/ai/command': { body: { reply: 'Here is a Streamlit app to review.', plans: [], draft, dropped: { refs: 0, actions: 0 } } },
    })
    renderApp('/apps')
    const dialog = await search('create a streamlit dashboard')
    await within(dialog).findAllByRole('option')
    await userEvent.keyboard('{Enter}')
    expect(await within(dialog).findByText("New Streamlit app 'Sales dashboard' from sales/app.py, Small")).toBeTruthy()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Review in New app' }))
    const name = (await screen.findByLabelText(/^Name/)) as HTMLInputElement
    expect(name.value).toBe('Sales dashboard')
    expect(screen.getByRole('button', { name: 'Streamlit, change' })).toBeTruthy()
    expect(screen.getByText('sales/app.py')).toBeTruthy()
    expect(screen.queryByRole('dialog', { name: 'Search and commands' })).toBeNull()
    expect(calls.count('POST', '/api/workspaces')).toBe(0)
  })

  it('still finds apps; with AI off there is no Ask AI row', async () => {
    api({ 'GET /api/ai/status': { body: aiOff } })
    renderApp('/apps')
    const dialog = await search('sal')
    const options = await within(dialog).findAllByRole('option')
    expect(options[0].textContent).toContain('Sales app')
    expect(within(dialog).queryByText(/Ask AI/)).toBeNull()
  })

  it('offers Ask about this cluster to admins only, with the report and the answer', async () => {
    const calls = api({
      'GET /api/me': { body: admin },
      'GET /api/admin/report': { body: { rows: [{ ref: 'failures/NoCapacity', title: 'Apps held by: No machine has room', value: 2, unit: 'apps', objects: ['javo-apps/a1', 'javo-apps/a2'] }] } },
      'POST /api/admin/ai/ask': { body: { answer: 'Two apps wait for a machine.', refs: ['failures/NoCapacity'], dropped: { refs: 0, actions: 0 } } },
    })
    renderApp('/apps')
    const dialog = await search('why are apps stuck')
    const row = await within(dialog).findByRole('option', { name: /Ask about this cluster/ })
    await userEvent.click(row)
    expect(await within(dialog).findByText('Two apps wait for a machine.')).toBeTruthy()
    expect(within(dialog).getByText('Apps held by: No machine has room')).toBeTruthy()
    expect(calls.calls.find((c) => c.path === '/api/admin/ai/ask')?.body).toEqual({ question: 'why are apps stuck' })
  })

  it('does not offer the cluster question to members', async () => {
    api()
    renderApp('/apps')
    const dialog = await search('why are apps stuck')
    await within(dialog).findAllByRole('option')
    expect(within(dialog).queryByText(/Ask about this cluster/)).toBeNull()
  })
})

describe('Build with pi in the palette', () => {
  const agent = { mode: 'platform', model: 'admin/model', template: 'pi', templateName: 'pi coding agent', available: true }
  const built = ws('create-a-streamlit-app', 'create a Streamlit app', 'Pending', allReady())

  it('starts a pi app on the request and opens its page', async () => {
    const calls = api({ 'GET /api/ai/agent': { body: agent }, 'POST /api/ai/agent/build': { status: 201, body: built }, 'GET /api/workspaces/create-a-streamlit-app': { body: built } })
    renderApp('/apps')
    const dialog = await search('create a Streamlit app')
    const row = await within(dialog).findByRole('option', { name: /Build with Pi: create a Streamlit app/ })
    // The agent's own logo, as on its app card, not a generic icon.
    expect(row.querySelector('.logo img')?.getAttribute('src')).toMatch(/pi/)
    await userEvent.click(row)
    await vi.waitFor(() => expect(calls.calls.find((c) => c.path === '/api/ai/agent/build')?.body).toEqual({ request: 'create a Streamlit app' }))
    await vi.waitFor(() => expect(location()).toBe('/apps/create-a-streamlit-app'))
  })

  it('is not offered without a model for the agent', async () => {
    api({ 'GET /api/ai/agent': { body: { ...agent, mode: 'login' } } })
    renderApp('/apps')
    const dialog = await search('create a Streamlit app')
    await within(dialog).findAllByRole('option')
    expect(within(dialog).queryByRole('option', { name: /Build with Pi/ })).toBeNull()
  })
})
