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
    await userEvent.type(within(card).getByRole('textbox', { name: 'Describe the app' }), 'A Streamlit dashboard of sales/data.csv')
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

describe('Describe it suggestion', () => {
  const suggestion = 'A Streamlit dashboard of sales/data.csv'

  async function typeInBox(text: string) {
    const box = within(await screen.findByLabelText('Describe it')).getByRole('textbox', { name: 'Describe the app' }) as HTMLTextAreaElement
    await userEvent.type(box, text)
    return box
  }
  const ghost = () => document.querySelector('.ghost-rest')?.textContent ?? ''

  it('shows the rest of one suggestion inside the box once typing pauses, and Tab accepts it', async () => {
    const calls = api({ 'POST /api/ai/agent/suggest': { body: { suggestions: [suggestion] } } })
    renderApp('/apps/new')
    const box = await typeInBox('A Streamlit dash')
    await vi.waitFor(() => expect(ghost()).toBe('board of sales/data.csv'))
    // One ask for the pause, not one per key, and no list of choices.
    expect(calls.count('POST', '/api/ai/agent/suggest')).toBe(1)
    expect(calls.calls.find((c) => c.path === '/api/ai/agent/suggest')?.body).toEqual({ text: 'A Streamlit dash' })
    expect(screen.queryByRole('listbox')).toBeNull()
    await userEvent.keyboard('{Tab}')
    expect(box.value).toBe(suggestion)
    expect(ghost()).toBe('')
    expect(document.activeElement).toBe(box)
    expect(calls.count('POST', '/api/ai/agent/build')).toBe(0)
  })

  it('keeps showing while the typing follows it, and goes on Escape or a different letter', async () => {
    api({ 'POST /api/ai/agent/suggest': { body: { suggestions: [suggestion] } } })
    renderApp('/apps/new')
    const box = await typeInBox('A Streamlit dash')
    await vi.waitFor(() => expect(ghost()).toBe('board of sales/data.csv'))
    await userEvent.type(box, 'bo')
    expect(ghost()).toBe('ard of sales/data.csv')
    await userEvent.keyboard('{Escape}')
    expect(ghost()).toBe('')
    expect(box.value).toBe('A Streamlit dashbo')
    await userEvent.type(box, 'x')
    expect(ghost()).toBe('')
  })

  it('goes when letters are deleted, and the next pause brings it back after the shorter text', async () => {
    api({ 'POST /api/ai/agent/suggest': { body: { suggestions: [suggestion] } } })
    renderApp('/apps/new')
    await typeInBox('A Streamlit dash')
    await vi.waitFor(() => expect(ghost()).toBe('board of sales/data.csv'))
    await userEvent.keyboard('{Backspace}')
    expect(ghost()).toBe('')
    await vi.waitFor(() => expect(ghost()).toBe('hboard of sales/data.csv'))
  })

  it('shows only while the caret is at the end of the text', async () => {
    api({ 'POST /api/ai/agent/suggest': { body: { suggestions: [suggestion] } } })
    renderApp('/apps/new')
    await typeInBox('A Streamlit dash')
    await vi.waitFor(() => expect(ghost()).toBe('board of sales/data.csv'))
    await userEvent.keyboard('{ArrowLeft}')
    expect(ghost()).toBe('')
    await userEvent.keyboard('{End}')
    expect(ghost()).toBe('board of sales/data.csv')
  })

  it('is not asked for under three characters', async () => {
    const calls = api({ 'POST /api/ai/agent/suggest': { body: { suggestions: [suggestion] } } })
    renderApp('/apps/new')
    await typeInBox('ab')
    await new Promise((r) => setTimeout(r, 700))
    expect(calls.count('POST', '/api/ai/agent/suggest')).toBe(0)
    expect(ghost()).toBe('')
  })
})
