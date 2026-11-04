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
    // The agent's logo on the button, and its name linking to its own site.
    expect(within(card).getByRole('button', { name: 'Build with Pi' }).querySelector('img')?.getAttribute('src')).toMatch(/pi/)
    expect(within(card).getByRole('link', { name: 'pi coding agent' }).getAttribute('href')).toBe('https://pi.dev')
    await userEvent.type(within(card).getByRole('combobox', { name: 'Describe the app' }), 'A Streamlit dashboard of sales/data.csv')
    await userEvent.click(within(card).getByRole('button', { name: 'Build with Pi' }))
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

describe('Describe it suggestions', () => {
  const suggestions = ['A Streamlit dashboard of sales/data.csv', 'A Streamlit dashboard of notes.csv by month']

  async function typeInBox(text: string) {
    const box = within(await screen.findByLabelText('Describe it')).getByRole('combobox', { name: 'Describe the app' }) as HTMLTextAreaElement
    await userEvent.type(box, text)
    return box
  }

  it('appear once typing pauses, and the keyboard picks one into the box', async () => {
    const calls = api({ 'POST /api/ai/agent/suggest': { body: { suggestions } } })
    renderApp('/apps/new')
    const box = await typeInBox('A Streamlit dash')
    const list = await screen.findByRole('listbox', { name: 'Suggestions' })
    expect(within(list).getAllByRole('option').map((o) => o.textContent)).toEqual(suggestions)
    // One ask for the pause, not one per key.
    expect(calls.count('POST', '/api/ai/agent/suggest')).toBe(1)
    expect(calls.calls.find((c) => c.path === '/api/ai/agent/suggest')?.body).toEqual({ text: 'A Streamlit dash' })
    expect(box.getAttribute('aria-expanded')).toBe('true')
    await userEvent.keyboard('{ArrowDown}{ArrowDown}')
    const second = within(list).getAllByRole('option')[1]
    expect(second.getAttribute('aria-selected')).toBe('true')
    expect(box.getAttribute('aria-activedescendant')).toBe(second.id)
    await userEvent.keyboard('{Enter}')
    expect(box.value).toBe(suggestions[1])
    expect(screen.queryByRole('listbox', { name: 'Suggestions' })).toBeNull()
    expect(calls.count('POST', '/api/ai/agent/build')).toBe(0)
  })

  it('close on Escape, leaving the text as typed; a click picks one', async () => {
    api({ 'POST /api/ai/agent/suggest': { body: { suggestions } } })
    renderApp('/apps/new')
    const box = await typeInBox('A Streamlit dash')
    await screen.findByRole('listbox', { name: 'Suggestions' })
    await userEvent.keyboard('{ArrowDown}{Escape}')
    expect(screen.queryByRole('listbox', { name: 'Suggestions' })).toBeNull()
    expect(box.value).toBe('A Streamlit dash')
    await userEvent.type(box, 'b')
    await userEvent.click(await screen.findByRole('option', { name: suggestions[0] }))
    expect(box.value).toBe(suggestions[0])
  })

  it('are not asked for under three characters', async () => {
    const calls = api({ 'POST /api/ai/agent/suggest': { body: { suggestions } } })
    renderApp('/apps/new')
    await typeInBox('ab')
    await new Promise((r) => setTimeout(r, 700))
    expect(calls.count('POST', '/api/ai/agent/suggest')).toBe(0)
    expect(screen.queryByRole('listbox')).toBeNull()
  })
})

describe('Describe it with a template already chosen', () => {
  it('stays closed behind a toggle, and opens on it', async () => {
    api()
    renderApp('/apps/new?template=jupyterlab')
    await screen.findByRole('button', { name: 'JupyterLab, change' })
    expect(screen.queryByLabelText('Describe it')).toBeNull()
    const toggle = screen.getByRole('button', { name: 'Build it with Pi' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(toggle)
    expect(await screen.findByLabelText('Describe it')).toBeTruthy()
  })
})
