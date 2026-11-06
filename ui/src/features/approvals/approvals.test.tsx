import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { allReady, me, plan, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const SUMMARY = 'Give Sales dashboard your secret STRIPE_KEY as STRIPE_API_KEY (it restarts)'

function attach(over: Record<string, unknown> = {}) {
  return plan({
    id: 'p1',
    source: 'mcp:pi',
    requestedBy: { app: 'pi' },
    expiresAt: new Date(Date.now() + 8 * 60_000).toISOString(),
    planned: {
      action: 'attach-secret',
      target: 'sales-dashboard',
      params: { secret: 'STRIPE_KEY', env: 'STRIPE_API_KEY' },
      summary: SUMMARY,
      destructive: false,
      changes: [{ field: 'STRIPE_API_KEY', from: '', to: 'secret STRIPE_KEY' }],
    },
    ...over,
  })
}

const stop = plan({
  id: 'p2',
  source: 'mcp:pi',
  requestedBy: { app: 'pi' },
  expiresAt: new Date(Date.now() + 5 * 60_000).toISOString(),
  planned: { action: 'delete', target: 'plots', summary: 'Delete Plots. Its home folder is kept.', destructive: true, changes: [{ field: 'app', from: 'Plots', to: 'deleted' }] },
})

describe('Approvals inbox', () => {
  it('lists pending plans with who asked, what changes and when it expires', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [attach()] } })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: SUMMARY })
    expect(within(card).getByText('Requested with the token of your app pi, by an agent that calls itself pi')).toBeTruthy()
    expect(within(card).getByLabelText('Changes').textContent).toContain('secret STRIPE_KEY')
    expect(within(card).getByText('Expires in 8 min')).toBeTruthy()
    expect(within(card).getByRole('button', { name: 'Approve' })).toBeTruthy()
    expect(within(card).getByRole('button', { name: 'Deny' })).toBeTruthy()
  })

  it('approves a plan, which then leaves the inbox', async () => {
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans': [{ body: [attach()] }, { body: [] }],
      'POST /api/plans/p1/approve': { body: attach({ state: 'applied' }) },
    })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: SUMMARY })
    await userEvent.click(within(card).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(screen.queryByRole('article', { name: SUMMARY })).toBeNull())
    expect(calls.count('POST', '/api/plans/p1/approve')).toBe(1)
    expect(await screen.findByText('Nothing is waiting for you.')).toBeTruthy()
  })

  it('keeps a plan that failed, with the reason', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans': [{ body: [attach()] }, { body: [] }],
      'POST /api/plans/p1/approve': { status: 422, body: { title: 'You have no secret called STRIPE_KEY.' } },
    })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: SUMMARY })
    await userEvent.click(within(card).getByRole('button', { name: 'Approve' }))
    expect(await within(card).findByText('You have no secret called STRIPE_KEY.')).toBeTruthy()
    expect(screen.getByRole('article', { name: SUMMARY })).toBeTruthy()
  })

  it('asks before a change that cannot be undone', async () => {
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans': [{ body: [stop] }, { body: [] }],
      'POST /api/plans/p2/approve': { body: { ...stop, state: 'applied' } },
    })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: stop.planned.summary })
    await userEvent.click(within(card).getByRole('button', { name: 'Approve' }))
    expect(calls.count('POST', '/api/plans/p2/approve')).toBe(0)
    const dialog = await screen.findByRole('alertdialog')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(calls.count('POST', '/api/plans/p2/approve')).toBe(1))
  })

  it('opens one plan from its approve link', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans/p1': { body: attach() } })
    renderApp('/approvals/p1')
    expect(await screen.findByText('Requested with the token of your app pi, by an agent that calls itself pi')).toBeTruthy()
    expect(screen.getByLabelText(SUMMARY)).toBeTruthy()
  })

  it('counts pending plans on the sidebar item', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [attach(), stop] }, 'GET /api/workspaces': { body: { items: [] } } })
    renderApp('/approvals')
    expect(await screen.findByRole('link', { name: 'Approvals, 2 waiting' })).toBeTruthy()
  })

  it('shows no count when nothing waits', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [] } })
    renderApp('/approvals')
    expect(await screen.findByText('Nothing is waiting for you.')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Approvals' })).toBeTruthy()
  })
})

describe('App page', () => {
  it("shows the app's pending plans in a banner", async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-dashboard': { body: ws('sales-dashboard', 'Sales dashboard', 'Running', allReady()) },
      'GET /api/workspaces/sales-dashboard/events': { body: { items: [] } },
      'GET /api/workspaces/sales-dashboard/logs': { body: { lines: [], restarts: 0 } },
      'GET /api/plans': { body: [attach(), stop] },
    })
    renderApp('/apps/sales-dashboard')
    const banner = await screen.findByRole('region', { name: 'Waiting for your approval' })
    expect(within(banner).getByRole('article', { name: SUMMARY })).toBeTruthy()
    expect(within(banner).queryByRole('article', { name: stop.planned.summary })).toBeNull()
  })
})
