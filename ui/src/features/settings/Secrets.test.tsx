import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SecretView } from '../../api'
import { location, mockApi, renderApp } from '../../test/api'
import { allReady, idlePolicy, me, stepsAt, templates, ws } from '../../test/fixtures'
import { choose } from '../../test/select'

afterEach(() => vi.unstubAllGlobals())

const VALUE = 'sk-test-PLACEHOLDER-ui-0451'
const openai: SecretView = {
  name: 'OPENAI_KEY',
  note: 'billing account',
  createdAt: '2026-10-01T09:00:00Z',
  updatedAt: '2026-10-20T09:00:00Z',
  apps: [{ name: 'dash', displayName: 'Sales dashboard' }],
  jobs: [],
}
const db: SecretView = { name: 'DB_PASSWORD', note: '', createdAt: '2026-10-02T09:00:00Z', updatedAt: '2026-10-02T09:00:00Z', apps: [], jobs: [] }

function setup(routes: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/secrets': { body: [openai, db] },
    ...routes,
  })
}

const row = async (name: string) =>
  within(await screen.findByRole('list', { name: 'Secrets' }))
    .getAllByRole('listitem')
    .find((r) => within(r).queryByRole('heading', { name }))!

describe('Settings, Secrets', () => {
  it('lists names, notes and the apps that use each', async () => {
    setup()
    renderApp('/settings/secrets')
    const o = await row('OPENAI_KEY')
    expect(within(o).getByText('billing account')).toBeTruthy()
    expect(within(o).getByText('Used by').nextSibling?.textContent).toBe('Sales dashboard')
    expect(within(await row('DB_PASSWORD')).getByText('Used by').nextSibling?.textContent).toBe('Nothing')
    expect(screen.getByText(/sharing an app does not share the secrets/)).toBeTruthy()
  })

  it('sends a new value once, then clears it and says it is never shown again', async () => {
    const made: SecretView = { ...db, name: 'NEW_KEY' }
    const api = setup({
      'POST /api/secrets': { status: 201, body: made },
      'GET /api/secrets': [{ body: [openai] }, { body: [openai, made] }],
    })
    renderApp('/settings/secrets')
    await userEvent.click(await screen.findByRole('button', { name: 'New secret' }))
    await userEvent.type(screen.getByLabelText('Name'), 'NEW_KEY')
    await userEvent.type(screen.getByLabelText('Value'), VALUE)
    await userEvent.click(screen.getByRole('button', { name: 'Save secret' }))
    expect(await screen.findByText('NEW_KEY saved. The value is never shown again.')).toBeTruthy()
    expect(api.calls.find((c) => c.method === 'POST')?.body).toEqual({ name: 'NEW_KEY', value: VALUE })
    // The form is gone and nothing on the page holds the value.
    expect(screen.queryByLabelText('Value')).toBeNull()
    expect(document.body.innerHTML).not.toContain(VALUE)
  })

  it('shows the field error the server gives', async () => {
    setup({ 'POST /api/secrets': { status: 422, body: { title: 'Invalid', errors: [{ location: 'body.name', message: 'JAVO_TOKEN is set by the platform. Pick another name.' }] } } })
    renderApp('/settings/secrets')
    await userEvent.click(await screen.findByRole('button', { name: 'New secret' }))
    await userEvent.type(screen.getByLabelText('Name'), 'JAVO_TOKEN')
    await userEvent.type(screen.getByLabelText('Value'), 'v')
    await userEvent.click(screen.getByRole('button', { name: 'Save secret' }))
    expect(await screen.findByText('JAVO_TOKEN is set by the platform. Pick another name.')).toBeTruthy()
    expect(screen.getByLabelText('Name').getAttribute('aria-invalid')).toBe('true')
  })

  it('replaces a value from an empty box, never one filled with the old value', async () => {
    const api = setup({ 'PUT /api/secrets/OPENAI_KEY': { body: { ...openai, updatedAt: '2026-11-02T09:00:00Z' } } })
    renderApp('/settings/secrets')
    await userEvent.click(within(await row('OPENAI_KEY')).getByRole('button', { name: 'Replace the value of OPENAI_KEY' }))
    const box = screen.getByLabelText('New value for OPENAI_KEY') as HTMLTextAreaElement
    expect(box.value).toBe('')
    expect(screen.getByText('Apps that use it restart to get the new value.')).toBeTruthy()
    await userEvent.type(box, VALUE)
    await userEvent.click(screen.getByRole('button', { name: 'Save value' }))
    expect(await screen.findByText('New value for OPENAI_KEY saved. The value is never shown again.')).toBeTruthy()
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ value: VALUE })
    expect(document.body.innerHTML).not.toContain(VALUE)
  })

  it('shows why a secret apps use cannot be deleted', async () => {
    const msg = 'In use by Sales dashboard. Take the secret off them first.'
    setup({ 'DELETE /api/secrets/OPENAI_KEY': { status: 409, body: { title: msg, status: 409 } } })
    renderApp('/settings/secrets')
    await userEvent.click(within(await row('OPENAI_KEY')).getByRole('button', { name: 'Delete OPENAI_KEY' }))
    await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Delete' }))
    expect(await screen.findByText(msg)).toBeTruthy()
    expect(await row('OPENAI_KEY')).toBeTruthy()
  })
})

describe('Environment variables of apps', () => {
  it('sends the typed values and secrets set under Advanced with the new app', async () => {
    const created = ws('streamlit-app', 'Streamlit app', 'Pending', stepsAt(0, 'Starting'))
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/workspaces': { body: { items: [] } },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/idle-stop': { body: idlePolicy },
      'GET /api/secrets': { body: [openai, db] },
      'POST /api/workspaces': { status: 201, body: created },
      'GET /api/workspaces/streamlit-app': { body: created },
    })
    renderApp('/apps/new?template=streamlit')
    await userEvent.click(await screen.findByRole('button', { name: 'Show advanced settings' }))
    const card = screen.getByRole('heading', { name: 'Environment variables' }).closest('.card') as HTMLElement
    await userEvent.click(within(card).getByRole('button', { name: 'Add a variable' }))
    await userEvent.type(within(card).getByLabelText('Name'), 'LOG_LEVEL')
    await userEvent.type(within(card).getByLabelText('Value'), 'debug')
    await userEvent.click(within(card).getByRole('button', { name: 'Add a variable' }))
    await choose(within(card).getAllByLabelText('From')[1], 'Secret')
    await userEvent.click(within(card).getByRole('combobox', { name: 'Secret' }))
    await userEvent.click(await screen.findByRole('option', { name: 'DB_PASSWORD' }))
    const name = within(card).getAllByLabelText('Name')[1]
    await userEvent.clear(name)
    await userEvent.type(name, 'PGPASSWORD')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(api.calls.find((c) => c.method === 'POST')?.body).toMatchObject({
      env: [
        { name: 'LOG_LEVEL', value: 'debug' },
        { name: 'PGPASSWORD', secret: 'DB_PASSWORD' },
      ],
    })
  })

  it('changes the variables of an app from its page, showing typed values and only the names of secrets', async () => {
    const dash = ws('dash', 'Sales dashboard', 'Running', allReady(), {
      env: [
        { name: 'LOG_LEVEL', value: 'debug' },
        { name: 'OPENAI_API_KEY', secret: 'OPENAI_KEY' },
      ],
    })
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/workspaces/dash': { body: dash },
      'GET /api/workspaces/dash/events': { body: { items: [] } },
      'GET /api/workspaces/dash/logs': { body: { lines: [], restarts: 0 } },
      'GET /api/storage': { body: {} },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/idle-stop': { body: idlePolicy },
      'GET /api/secrets': { body: [openai, db] },
      'PUT /api/workspaces/dash/env': { body: { ...dash, env: [] } },
    })
    renderApp('/apps/dash')
    expect(await screen.findByText('LOG_LEVEL=debug')).toBeTruthy()
    expect(screen.getByText('OPENAI_API_KEY from OPENAI_KEY')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Change variables' }))
    expect(screen.getByText('Saving restarts the app with the change.')).toBeTruthy()
    expect((screen.getAllByLabelText('Value')[0] as HTMLInputElement).value).toBe('debug')
    await userEvent.click(screen.getByRole('button', { name: 'Remove OPENAI_API_KEY' }))
    await userEvent.click(screen.getByRole('button', { name: 'Remove LOG_LEVEL' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save variables' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ env: [] }))
  })

  it('offers no variables on the preview of a pull request', async () => {
    const preview = ws('site--pr-3', 'Site PR 3', 'Running', allReady(), { previewOf: { name: 'site', pullNumber: 3 } })
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/workspaces/site--pr-3': { body: preview },
      'GET /api/workspaces/site--pr-3/events': { body: { items: [] } },
      'GET /api/workspaces/site--pr-3/logs': { body: { lines: [], restarts: 0 } },
      'GET /api/storage': { body: {} },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/idle-stop': { body: idlePolicy },
    })
    renderApp('/apps/site--pr-3')
    expect(await screen.findByText('Template')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Change variables' })).toBeNull()
  })
})
