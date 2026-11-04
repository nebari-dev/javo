import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { allReady, idlePolicy, me, stepsAt, stoppedIdle, templates, ws } from '../../test/fixtures'
import { choose, optionsOf } from '../../test/select'

afterEach(() => vi.unstubAllGlobals())

describe('New app Stop when idle', () => {
  const created = ws('streamlit-app', 'Streamlit app', 'Pending', stepsAt(0, 'Starting'))
  // The template list gives each template the default the policy leads to, unless told otherwise.
  function setup(policy = idlePolicy, templateDefault = policy.defaultMinutes) {
    return mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items: templates.map((t) => ({ ...t, defaultIdleStopMinutes: templateDefault })) } },
      'GET /api/workspaces': { body: { items: [] } },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/idle-stop': { body: policy },
      'POST /api/workspaces': { status: 201, body: created },
      'GET /api/workspaces/streamlit-app': { body: created },
    })
  }
  const createBody = (api: ReturnType<typeof setup>) => api.calls.find((c) => c.method === 'POST')?.body
  async function openAdvanced() {
    renderApp('/apps/new?template=streamlit')
    await userEvent.click(await screen.findByRole('button', { name: 'Show advanced settings' }))
  }
  async function create() {
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
  }

  it('shows the admin default, on, and leaves it out of the request', async () => {
    const api = setup()
    await openAdvanced()
    expect(screen.getByRole('switch', { name: 'Stop when idle' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('combobox', { name: 'Stop after' }).textContent).toBe('60 minutes')
    await create()
    expect(createBody(api)).not.toHaveProperty('idleStopMinutes')
  })

  it('sends the time picked, offering only the allowed range', async () => {
    const api = setup({ ...idlePolicy, minMinutes: 30, maxMinutes: 240 })
    await openAdvanced()
    const after = screen.getByRole('combobox', { name: 'Stop after' })
    expect(await optionsOf(after)).toEqual(['30 minutes', '60 minutes', '2 hours', '4 hours'])
    await choose(after, '2 hours')
    await create()
    expect(createBody(api)).toMatchObject({ idleStopMinutes: 120 })
  })

  it('turns it off', async () => {
    const api = setup()
    await openAdvanced()
    await userEvent.click(screen.getByRole('switch', { name: 'Stop when idle' }))
    expect(screen.queryByRole('combobox', { name: 'Stop after' })).toBeNull()
    await create()
    expect(createBody(api)).toMatchObject({ idleStopMinutes: 0 })
  })

  it('has no switch when people may not turn it off', async () => {
    setup({ ...idlePolicy, canTurnOff: false })
    await openAdvanced()
    expect(screen.queryByRole('switch', { name: 'Stop when idle' })).toBeNull()
    expect(screen.getByRole('combobox', { name: 'Stop after' })).toBeTruthy()
  })

  it('starts off when the admin default is off, and turning it on picks an hour', async () => {
    const api = setup({ ...idlePolicy, defaultMinutes: 0 })
    await openAdvanced()
    const sw = screen.getByRole('switch', { name: 'Stop when idle' })
    expect(sw.getAttribute('aria-checked')).toBe('false')
    await userEvent.click(sw)
    expect(screen.getByRole('combobox', { name: 'Stop after' }).textContent).toBe('60 minutes')
    await create()
    expect(createBody(api)).toMatchObject({ idleStopMinutes: 60 })
  })

  it("shows the template's own default, and sends Off when turned off", async () => {
    const api = setup({ ...idlePolicy, defaultMinutes: 0 }, 10)
    await openAdvanced()
    expect(screen.getByRole('switch', { name: 'Stop when idle' }).getAttribute('aria-checked')).toBe('true')
    expect(screen.getByRole('combobox', { name: 'Stop after' }).textContent).toBe('10 minutes')
    await userEvent.click(screen.getByRole('switch', { name: 'Stop when idle' }))
    await create()
    expect(createBody(api)).toMatchObject({ idleStopMinutes: 0 })
  })

  it("sends the admin's default when picked over the template's own", async () => {
    const api = setup(idlePolicy, 10)
    await openAdvanced()
    await choose(screen.getByRole('combobox', { name: 'Stop after' }), '60 minutes')
    await create()
    expect(createBody(api)).toMatchObject({ idleStopMinutes: 60 })
  })
})

describe('App page Stop when idle', () => {
  const app = ws('plots', 'Plots', 'Running', allReady(), { idleStopMinutes: 60 })
  it('shows the timeout and changes it without a restart', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/idle-stop': { body: idlePolicy },
      'GET /api/workspaces/plots': { body: app },
      'PUT /api/workspaces/plots/idle-stop': { body: { ...app, idleStopMinutes: 0 } },
    })
    renderApp('/apps/plots')
    expect(await screen.findByText('60 minutes')).toBeTruthy()
    await userEvent.click(await screen.findByRole('button', { name: 'Change when it stops' }))
    expect(screen.getByText(/keeps running/)).toBeTruthy()
    await userEvent.click(screen.getByRole('switch', { name: 'Stop when idle' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ idleStopMinutes: 0 }))
    expect(await screen.findByText('Off')).toBeTruthy()
  })

  it('says a stopped app was stopped for being idle', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/idle-stop': { body: idlePolicy },
      'GET /api/workspaces/plots': { body: { ...app, phase: 'Stopped', steps: stoppedIdle('60 minutes'), stoppedIdleAfter: 60 } },
    })
    renderApp('/apps/plots')
    expect(await screen.findByText('Stopped after 60 minutes without use')).toBeTruthy()
  })
})

describe('App card and row Stop when idle', () => {
  it('says why a stopped app stopped, in both views', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/shared': { body: { items: [] } },
      'GET /api/workspaces': {
        body: { items: [ws('plots', 'Plots', 'Stopped', stoppedIdle('60 minutes'), { idleStopMinutes: 60, stoppedIdleAfter: 60 })] },
      },
    })
    renderApp('/apps')
    expect(await screen.findByText('Stopped after 60 minutes without use')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'List' }))
    expect(await screen.findByRole('table')).toBeTruthy()
    expect(screen.getByText('Stopped after 60 minutes without use')).toBeTruthy()
  })
})
