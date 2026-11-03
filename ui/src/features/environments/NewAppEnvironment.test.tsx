import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { engines, jobTemplate, me, printTimeDetail, stepsAt, templateCategories, templates, ws } from '../../test/fixtures'
import { choose } from '../../test/select'

afterEach(() => vi.unstubAllGlobals())

const offered = { available: true, items: [{ id: 'ws-1', name: 'sales', owner: 'bob' }] }

function base(environments: unknown, extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items: [...templates, jobTemplate], categories: templateCategories } },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/sizes': { body: { items: [] } },
    'GET /api/engines': { body: { items: engines } },
    'GET /api/jobs': { body: { items: [] } },
    'GET /api/environments': { body: environments },
    ...extra,
  })
}

const environmentCard = () => screen.queryByRole('heading', { name: 'Environment' })

describe('Environments on New app', () => {
  it('offers nothing when the platform has no environment manager', async () => {
    base({ available: false, items: [] })
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Streamlit/ }))
    await screen.findByRole('button', { name: /^Code: / })
    expect(environmentCard()).toBeNull()
  })

  it('sends the picked environment with the new app', async () => {
    const created = ws('dash', 'Dash', 'Pending', stepsAt(0, 'Starting'), {
      environment: { source: 'nebi', id: 'ws-1', name: 'sales', version: '3' },
    })
    const api = base(offered, {
      'POST /api/workspaces': { status: 201, body: created },
      'GET /api/workspaces/dash': { body: created },
    })
    renderApp('/apps/new?template=streamlit')
    const card = (await screen.findByRole('heading', { name: 'Environment' })).closest('.part') as HTMLElement
    await choose(within(card).getByLabelText('Environment'), 'sales (bob)')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/dash'))
    expect(api.calls.find((c) => c.method === 'POST' && c.path === '/api/workspaces')?.body).toMatchObject({ environment: { id: 'ws-1' } })
  })

  it('says why the list is empty, and what the app runs with instead', async () => {
    const why = "Environments need a sign-in with your organization's account; this session has none."
    base({ available: true, items: [], message: why })
    renderApp('/apps/new?template=streamlit')
    const card = (await screen.findByRole('heading', { name: 'Environment' })).closest('.part') as HTMLElement
    expect(within(card).getByText(`${why} The app runs with its template's own packages.`)).toBeTruthy()
  })

  it('says what the app runs with when there are no environments', async () => {
    base({ available: true, items: [] })
    renderApp('/apps/new?template=streamlit')
    const card = (await screen.findByRole('heading', { name: 'Environment' })).closest('.part') as HTMLElement
    expect(
      within(card).getByText("You have no environments yet, and none are shared with you. The app runs with its template's own packages."),
    ).toBeTruthy()
  })

  it('says the app keeps the environment as it is when the app is made', async () => {
    base(offered)
    renderApp('/apps/new?template=streamlit')
    const card = (await screen.findByRole('heading', { name: 'Environment' })).closest('.part') as HTMLElement
    expect(
      within(card).getByText(
        "The app gets the environment's packages as they are now. Changes made to the environment later do not reach this app.",
      ),
    ).toBeTruthy()
  })
})

describe('Environments on New job', () => {
  it('sends the picked environment with the new job', async () => {
    const api = base(offered, {
      'POST /api/jobs': { status: 201, body: printTimeDetail },
      'GET /api/jobs/print-the-time': { body: printTimeDetail },
    })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: 'Command' }))
    await userEvent.type(screen.getByLabelText('Command'), 'python etl.py')
    await userEvent.type(screen.getByLabelText('Name'), 'Print the time')
    const card = (await screen.findByRole('heading', { name: 'Environment' })).closest('.part') as HTMLElement
    await choose(within(card).getByLabelText('Environment'), 'sales (bob)')
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/print-the-time'))
    expect(api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body).toMatchObject({ environment: { id: 'ws-1' } })
  })

  it('offers nothing when the platform has no environment manager', async () => {
    base({ available: false, items: [] })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: 'Command' }))
    await screen.findByLabelText('Command')
    expect(environmentCard()).toBeNull()
  })
})
