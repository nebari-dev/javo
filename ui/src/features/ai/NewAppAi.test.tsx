import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { aiOff, aiOn, me, templates } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const filled = {
  draft: { displayName: 'Sales', template: 'streamlit', file: 'e2e/hello.py' },
  setBy: ['displayName', 'template', 'file'],
  errors: [],
  model: 'claude-opus-5-5',
}

function api(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items: templates } },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/sizes': { body: { items: [] } },
    'GET /api/ai/status': { body: aiOn },
    'POST /api/ai/draft': { body: filled },
    ...extra,
  })
}

async function describeIt(text: string) {
  const card = await screen.findByLabelText('Describe it')
  await userEvent.type(within(card).getByRole('textbox', { name: 'Describe the app' }), text)
  await userEvent.click(within(card).getByRole('button', { name: 'Fill the form' }))
}

describe('Describe it', () => {
  it('is not there with AI off', async () => {
    api({ 'GET /api/ai/status': { body: aiOff } })
    renderApp('/apps/new')
    await screen.findByRole('radio', { name: /Streamlit/ })
    expect(screen.queryByLabelText('Describe it')).toBeNull()
  })

  it('fills the normal form, marks what it filled and creates nothing', async () => {
    const calls = api()
    renderApp('/apps/new')
    expect(await screen.findByText('AI fills the form; nothing is created until you press Create.')).toBeTruthy()
    await describeIt('A Streamlit app from e2e/hello.py called Sales')
    expect(await screen.findByRole('button', { name: 'Streamlit, change' })).toBeTruthy()
    const name = screen.getByLabelText(/^Name/) as HTMLInputElement
    expect(name.value).toBe('Sales')
    expect(screen.getByText('e2e/hello.py')).toBeTruthy()
    expect(screen.getAllByText('Filled by AI')).toHaveLength(3)
    expect(calls.count('POST', '/api/workspaces')).toBe(0)
    expect(calls.calls.find((c) => c.path === '/api/ai/draft')?.body).toEqual({ text: 'A Streamlit app from e2e/hello.py called Sales', kind: 'app' })
    await userEvent.type(name, ' app')
    expect(screen.getAllByText('Filled by AI')).toHaveLength(2)
  })

  it('shows the draft field errors like a create, and holds Create until they are fixed', async () => {
    api({
      'POST /api/ai/draft': {
        body: { ...filled, draft: { ...filled.draft, file: '../etc/passwd' }, errors: [{ location: 'body.file', message: 'Pick a file inside your home folder.' }] },
      },
    })
    renderApp('/apps/new')
    await describeIt('something odd')
    expect(await screen.findByText('Pick a file inside your home folder.')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Create app' }) as HTMLButtonElement).disabled).toBe(true)
  })
})

describe('Describe it with a template already chosen', () => {
  it('stays closed behind a toggle, and opens on it', async () => {
    api()
    renderApp('/apps/new?template=jupyterlab')
    await screen.findByRole('button', { name: 'JupyterLab, change' })
    expect(screen.queryByLabelText('Describe it')).toBeNull()
    const toggle = screen.getByRole('button', { name: 'Describe it with AI' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(toggle)
    expect(await screen.findByLabelText('Describe it')).toBeTruthy()
  })
})
