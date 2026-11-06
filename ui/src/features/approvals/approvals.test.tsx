import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { allReady, me, plan, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const inMinutes = (n: number) => new Date(Date.now() + n * 60_000).toISOString()
const minutesAgo = (n: number) => new Date(Date.now() - n * 60_000).toISOString()

/** Pi, from inside the app Sales agent, asks to give Sales dashboard the secret STRIPE_KEY. */
function attach(over: Record<string, unknown> = {}) {
  return plan({
    id: 'p1',
    source: 'mcp:pi',
    requestedBy: { app: 'sales-agent', appName: 'Sales agent', appKind: 'pi' },
    createdAt: minutesAgo(2),
    expiresAt: inMinutes(8),
    planned: {
      action: 'attach-secret',
      target: 'sales-dashboard',
      targetName: 'Sales dashboard',
      params: { secret: 'STRIPE_KEY', env: 'STRIPE_API_KEY' },
      summary: 'Give Sales dashboard your secret STRIPE_KEY as STRIPE_API_KEY (it restarts)',
      destructive: false,
      changes: [{ field: 'STRIPE_API_KEY', from: '', to: 'secret STRIPE_KEY' }],
    },
    ...over,
  })
}

/** Claude Code, with the API token laptop, asks to restart Report builder. */
const restart = plan({
  id: 'p3',
  source: 'mcp:claude-code',
  requestedBy: { token: 'laptop' },
  createdAt: minutesAgo(5),
  expiresAt: inMinutes(12),
  planned: {
    action: 'restart',
    target: 'report-builder',
    targetName: 'Report builder',
    summary: 'Restart Report builder',
    destructive: false,
    changes: [{ field: 'state', from: 'Running', to: 'Restarted' }],
  },
})

/** Claude Code asks to delete Research lab, which cannot be undone. */
const del = plan({
  id: 'p2',
  source: 'mcp:claude-code',
  requestedBy: { token: 'laptop' },
  createdAt: minutesAgo(9),
  expiresAt: inMinutes(3),
  planned: {
    action: 'delete',
    target: 'research-lab',
    targetName: 'Research lab',
    summary: 'Delete Research lab. Its home folder is kept.',
    destructive: true,
    changes: [{ field: 'app', from: 'Research lab', to: 'deleted' }],
  },
})

const ATTACH_Q = 'Let Sales dashboard use your secret STRIPE_KEY?'

const chips = (card: HTMLElement) => within(within(card).getByRole('list', { name: 'What happens' })).getAllByRole('listitem').map((li) => li.textContent)

describe('Approvals inbox', () => {
  it('asks a question built from the plan, with who asked, what happens and when it expires', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [attach()] } })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: ATTACH_Q })
    expect(within(card).getByRole('heading', { name: ATTACH_Q })).toBeTruthy()
    expect(within(card).getByText('Pi')).toBeTruthy()
    expect(within(card).getByText('in Sales agent, 2 minutes ago')).toBeTruthy()
    expect(within(card).getByText('Sales dashboard will be able to use the secret. Pi will not see it.')).toBeTruthy()
    expect(chips(card)).toEqual(['Sales dashboard restarts', 'Hidden from Pi', 'You can remove it later'])
    expect(within(card).getByText('8 min left')).toBeTruthy()
    expect(within(card).getByRole('button', { name: 'Give access' })).toBeTruthy()
    expect(within(card).getByRole('button', { name: 'Decline' })).toBeTruthy()
    expect(screen.getByText('1 request needs your decision. Nothing happens until you decide.')).toBeTruthy()
  })

  it('keeps the technical details behind Details', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [attach()] } })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: ATTACH_Q })
    expect(within(card).queryByText('STRIPE_API_KEY')).toBeNull()
    await userEvent.click(within(card).getByRole('button', { name: 'Details' }))
    const details = within(card).getByRole('group', { name: 'Details' })
    expect(within(details).getByText('Name inside the app').nextElementSibling?.textContent).toBe('STRIPE_API_KEY')
    expect(within(details).getByLabelText('Changes').textContent).toContain('secret STRIPE_KEY')
    expect(within(details).getByText('The token of your app Sales agent')).toBeTruthy()
    expect(within(details).getByText('p1')).toBeTruthy()
  })

  it('names each action in its question, chips and main button', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [restart, del] } })
    renderApp('/approvals')
    const r = await screen.findByRole('article', { name: 'Restart Report builder?' })
    expect(within(r).getByText('Claude Code')).toBeTruthy()
    expect(within(r).getByText('with your API token laptop, 5 minutes ago')).toBeTruthy()
    expect(chips(r)).toEqual(['Offline for a moment', 'Files and settings kept'])
    expect(within(r).getByRole('button', { name: 'Restart' })).toBeTruthy()
    const d = screen.getByRole('article', { name: 'Delete Research lab?' })
    expect(chips(d)).toEqual(['App and link removed', 'Home folder kept', "Can't be undone"])
    expect(within(d).getByRole('button', { name: 'Delete Research lab' })).toBeTruthy()
  })

  it("shows each agent's own logo, and a plain icon for one it does not know", async () => {
    const other = { ...restart, id: 'p9', source: 'mcp:helper', requestedBy: { token: 'laptop' }, planned: { ...restart.planned, target: 'other', targetName: 'Other' } }
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [attach(), restart, other] } })
    renderApp('/approvals')
    const pi = await screen.findByRole('article', { name: ATTACH_Q })
    expect(pi.querySelector('.agent img')?.getAttribute('src')).toMatch(/pi/)
    const cc = screen.getByRole('article', { name: 'Restart Report builder?' })
    expect((cc.querySelector('.agent .mark') as HTMLElement | null)?.style.maskImage).toMatch(/claudecode/)
    const unknown = screen.getByRole('article', { name: 'Restart Other?' })
    expect(within(unknown).getByText('helper')).toBeTruthy()
    expect(unknown.querySelector('.agent .mark, .agent img')).toBeNull()
    expect(unknown.querySelector('.agent svg')).toBeTruthy()
  })

  it('gives access, says what happened, and leaves nothing waiting', async () => {
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans': [{ body: [attach()] }, { body: [] }],
      'POST /api/plans/p1/approve': { body: attach({ state: 'applied' }) },
    })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: ATTACH_Q })
    await userEvent.click(within(card).getByRole('button', { name: 'Give access' }))
    expect(await within(card).findByText('Access given. Sales dashboard is restarting with the secret.')).toBeTruthy()
    expect(screen.getAllByRole('status').some((s) => s.textContent === 'Access given. Sales dashboard is restarting with the secret.')).toBe(true)
    expect(within(card).queryByRole('button', { name: 'Give access' })).toBeNull()
    expect(calls.count('POST', '/api/plans/p1/approve')).toBe(1)
    expect(await screen.findByText('All decided. Nothing else needs you.')).toBeTruthy()
  })

  it('declines, and nothing changes', async () => {
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans': [{ body: [restart] }, { body: [] }],
      'POST /api/plans/p3/reject': { body: { ...restart, state: 'rejected' } },
    })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: 'Restart Report builder?' })
    await userEvent.click(within(card).getByRole('button', { name: 'Decline' }))
    expect(await within(card).findByText('Declined. Nothing changed.')).toBeTruthy()
    expect(calls.count('POST', '/api/plans/p3/reject')).toBe(1)
    expect(calls.count('POST', '/api/plans/p3/approve')).toBe(0)
  })

  it('keeps a request that failed, with the reason', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans': [{ body: [attach()] }, { body: [] }],
      'POST /api/plans/p1/approve': { status: 422, body: { title: 'You have no secret called STRIPE_KEY.' } },
    })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: ATTACH_Q })
    await userEvent.click(within(card).getByRole('button', { name: 'Give access' }))
    expect(await within(card).findByText("Couldn't give access: You have no secret called STRIPE_KEY. Nothing changed.")).toBeTruthy()
    expect(screen.getByRole('article', { name: ATTACH_Q })).toBeTruthy()
  })

  it('asks before a delete, naming the app on both buttons', async () => {
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans': [{ body: [del] }, { body: [] }],
      'POST /api/plans/p2/approve': { body: { ...del, state: 'applied' } },
    })
    renderApp('/approvals')
    const card = await screen.findByRole('article', { name: 'Delete Research lab?' })
    await userEvent.click(within(card).getByRole('button', { name: 'Delete Research lab' }))
    expect(calls.count('POST', '/api/plans/p2/approve')).toBe(0)
    const dialog = await screen.findByRole('alertdialog', { name: 'Delete Research lab for good?' })
    await userEvent.click(within(dialog).getByRole('button', { name: 'Keep Research lab' }))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(calls.count('POST', '/api/plans/p2/approve')).toBe(0)
    await userEvent.click(within(card).getByRole('button', { name: 'Delete Research lab' }))
    await userEvent.click(within(await screen.findByRole('alertdialog')).getByRole('button', { name: 'Delete Research lab' }))
    await waitFor(() => expect(calls.count('POST', '/api/plans/p2/approve')).toBe(1))
    expect(await within(card).findByText('Deleted. Files in its home folder are kept.')).toBeTruthy()
  })

  it('counts waiting requests on the sidebar item', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [attach(), del] }, 'GET /api/workspaces': { body: { items: [] } } })
    renderApp('/approvals')
    expect(await screen.findByRole('link', { name: 'Approvals, 2 waiting' })).toBeTruthy()
    expect(screen.getByText('2 requests need your decision. Nothing happens until you decide.')).toBeTruthy()
  })

  it('says what lands here when nothing waits, with no count', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans': { body: [] } })
    renderApp('/approvals')
    expect(await screen.findByRole('heading', { name: 'No approvals waiting' })).toBeTruthy()
    expect(screen.getByText('Agents ask here before they use your secrets or start, stop, restart, resize or delete an app.')).toBeTruthy()
    expect(screen.getByText("Can't be undone")).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Approvals' })).toBeTruthy()
  })
})

describe('One request', () => {
  it('opens from its approve link with who asks, what happens and how long it waits', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans/p1': { body: attach() }, 'GET /api/plans': { body: [attach(), del] } })
    renderApp('/approvals/p1')
    expect(await screen.findByRole('heading', { level: 1, name: ATTACH_Q })).toBeTruthy()
    expect(screen.getByText(/working in Sales agent, asks:/)).toBeTruthy()
    expect(screen.getByText('Only Sales dashboard can read the secret. Pi never sees it.')).toBeTruthy()
    expect(screen.getByText("Expires in 8 min. If you don't decide, nothing changes.")).toBeTruthy()
    expect(screen.getByRole('link', { name: 'See all approvals (2)' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Give access' })).toBeTruthy()
  })

  it('says so when the request is already decided', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans/p3': { body: { ...restart, state: 'rejected' } } })
    renderApp('/approvals/p3')
    expect(await screen.findByText('Already decided. You declined it. Nothing changed.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Restart' })).toBeNull()
  })
})

describe('App page', () => {
  it("shows the app's waiting requests in a banner, with the actions inline", async () => {
    let approved = false
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-dashboard': { body: ws('sales-dashboard', 'Sales dashboard', 'Running', allReady()) },
      'GET /api/workspaces/sales-dashboard/events': { body: { items: [] } },
      'GET /api/workspaces/sales-dashboard/logs': { body: { lines: [], restarts: 0 } },
      'GET /api/plans': () => ({ body: approved ? [del] : [attach(), del] }),
      'POST /api/plans/p1/approve': () => {
        approved = true
        return { body: attach({ state: 'applied' }) }
      },
    })
    renderApp('/apps/sales-dashboard')
    const banner = await screen.findByRole('article', { name: 'Pi asks: let this app use your secret STRIPE_KEY?' })
    expect(banner.querySelector('.agent img')?.getAttribute('src')).toMatch(/pi/)
    expect(chips(banner)).toEqual(['Sales dashboard restarts', 'Hidden from Pi', 'You can remove it later'])
    expect(screen.queryByRole('article', { name: /Research lab/ })).toBeNull()
    await userEvent.click(within(banner).getByRole('button', { name: 'Give access' }))
    expect(await within(banner).findByText('Access given. Sales dashboard is restarting with the secret.')).toBeTruthy()
    expect(calls.count('POST', '/api/plans/p1/approve')).toBe(1)
  })
})
