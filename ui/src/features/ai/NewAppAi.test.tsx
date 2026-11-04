import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { aiOn, allReady, me, templates, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const agent = { mode: 'platform', model: 'admin/model', template: 'pi', templateName: 'pi coding agent', available: true }
const built = ws('sales-dashboard', 'Sales dashboard', 'Pending', allReady())

function api(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items: templates } },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/sizes': { body: { items: [] } },
    'GET /api/ai/status': { body: aiOn },
    'GET /api/ai/agent': { body: agent },
    'POST /api/ai/agent/build': { status: 201, body: built },
    'GET /api/workspaces/sales-dashboard': { body: built },
    ...extra,
  })
}

describe('Describe it', () => {
  it('hands the description to the coding agent, which builds the app', async () => {
    const calls = api()
    renderApp('/apps/new')
    const card = await screen.findByLabelText('Describe it')
    await userEvent.type(within(card).getByRole('textbox', { name: 'Describe the app' }), 'A Streamlit dashboard of sales/data.csv')
    await userEvent.click(within(card).getByRole('button', { name: 'Build with pi' }))
    await waitFor(() => expect(calls.calls.find((c) => c.path === '/api/ai/agent/build')?.body).toEqual({ request: 'A Streamlit dashboard of sales/data.csv' }))
    await waitFor(() => expect(location()).toBe('/apps/sales-dashboard'))
    expect(calls.count('POST', '/api/ai/draft')).toBe(0)
  })

  it('is not there without a model for the agent', async () => {
    api({ 'GET /api/ai/agent': { body: { ...agent, mode: 'login' } } })
    renderApp('/apps/new')
    await screen.findByRole('radio', { name: /Streamlit/ })
    expect(screen.queryByLabelText('Describe it')).toBeNull()
  })
})

describe('Describe it with a template already chosen', () => {
  it('stays closed behind a toggle, and opens on it', async () => {
    api()
    renderApp('/apps/new?template=jupyterlab')
    await screen.findByRole('button', { name: 'JupyterLab, change' })
    expect(screen.queryByLabelText('Describe it')).toBeNull()
    const toggle = screen.getByRole('button', { name: 'Build it with pi' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(toggle)
    expect(await screen.findByLabelText('Describe it')).toBeTruthy()
  })
})
