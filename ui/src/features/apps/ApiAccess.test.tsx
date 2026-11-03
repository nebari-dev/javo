import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { agentTemplate, allReady, me, stepsAt, templates, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const HELP = 'Gives the app JAVO_URL and JAVO_TOKEN so it can call the Javo API as you.'
const ACTS = 'This app can call the API as you. Anyone you share it with can make it act as you.'

describe('New app API access', () => {
  const created = ws('claude-code-app', 'Claude Code app', 'Pending', stepsAt(0, 'Starting'))
  function setup(items = [...templates, agentTemplate]) {
    return mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items } },
      'GET /api/workspaces': { body: { items: [] } },
      'GET /api/sizes': { body: { items: [] } },
      'POST /api/workspaces': { status: 201, body: created },
      'GET /api/workspaces/claude-code-app': { body: created },
    })
  }
  const createBody = (api: ReturnType<typeof setup>) => api.calls.find((c) => c.method === 'POST')?.body

  it('sits behind Advanced, closed, with the template default chosen', async () => {
    setup()
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Claude Code/ }))
    expect(screen.queryByRole('radiogroup', { name: 'API access' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Show advanced settings' }))
    expect(screen.getByRole('radio', { name: 'Read and write' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByText(HELP)).toBeTruthy()
  })

  it('sends the access picked', async () => {
    const api = setup()
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Claude Code/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Show advanced settings' }))
    await userEvent.click(screen.getByRole('radio', { name: 'Read' }))
    expect(screen.getByText(/Anyone you share it with can make it act as you/)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/claude-code-app'))
    expect(createBody(api)).toEqual({ displayName: 'Claude Code app', template: 'claude-code', apiAccess: 'Read' })
  })

  it('leaves out the access when it is the default', async () => {
    const api = setup()
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Claude Code/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/claude-code-app'))
    expect(createBody(api)).toEqual({ displayName: 'Claude Code app', template: 'claude-code' })
  })

  it('offers no more than the template allows', async () => {
    setup([{ ...agentTemplate, apiAccessDefault: 'Read', maxApiAccess: 'Read' }])
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Claude Code/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Show advanced settings' }))
    expect(screen.getByRole('radio', { name: 'Read and write' }).getAttribute('aria-disabled')).toBe('true')
  })
})

describe('App page API access', () => {
  const app = ws('agent', 'Agent', 'Running', allReady(), { template: 'claude-code', kind: 'claude-code', file: '', apiAccess: 'ReadWrite' })
  it('shows the access and changes it, saying the app restarts', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/templates': { body: { items: [agentTemplate] } },
      'GET /api/workspaces/agent': { body: app },
      'PUT /api/workspaces/agent/api-access': { body: { ...app, apiAccess: 'Read' } },
    })
    renderApp('/apps/agent')
    expect(await screen.findByText('Read and write')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Change API access' }))
    expect(screen.getByText(/restarts/)).toBeTruthy()
    expect(screen.getByText(/Anyone you share it with can make it act as you/)).toBeTruthy()
    await userEvent.click(screen.getByRole('radio', { name: 'Read' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save API access' }))
    await waitFor(() => expect(screen.queryByRole('radiogroup', { name: 'API access' })).toBeNull())
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ apiAccess: 'Read' })
    expect(screen.getByText('Read')).toBeTruthy()
  })
})

describe('Sharing an app with API access', () => {
  const app = ws('agent', 'Agent', 'Running', allReady(), { apiAccess: 'ReadWrite' })
  const view = (access: string, apiAccess: string) => ({
    access,
    maxAccess: 'Public',
    users: [],
    groups: [],
    link: 'https://apps.example.com/u/alice/agent/',
    levels: [
      { value: 'Restricted', label: 'Only people added' },
      { value: 'SignedIn', label: 'Anyone signed in' },
      { value: 'Public', label: 'Anyone with the link' },
    ],
    apiAccess,
    ...(apiAccess === 'None' ? {} : { actsAsOwner: ACTS }),
  })
  function open(sharing: object) {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/agent': { body: app },
      'GET /api/workspaces/agent/sharing': { body: sharing },
    })
    renderApp('/apps/agent')
  }

  it('warns when an app that can write as you is shared', async () => {
    open(view('SignedIn', 'ReadWrite'))
    await userEvent.click(await screen.findByRole('tab', { name: 'Sharing' }))
    const banner = (await screen.findByText(ACTS)).closest('.banner')
    expect(banner?.classList.contains('warn')).toBe(true)
  })

  it('only notes it while the app is private or can only read', async () => {
    open(view('Private', 'ReadWrite'))
    await userEvent.click(await screen.findByRole('tab', { name: 'Sharing' }))
    const banner = (await screen.findByText(ACTS)).closest('.banner')
    expect(banner?.classList.contains('info')).toBe(true)
  })

  it('says nothing for an app without API access', async () => {
    open(view('SignedIn', 'None'))
    await userEvent.click(await screen.findByRole('tab', { name: 'Sharing' }))
    await screen.findByRole('radiogroup', { name: 'General access' })
    expect(screen.queryByText(ACTS)).toBeNull()
  })
})
