import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { AI_TEXT, me, plan } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const fromAgent = plan({ source: 'mcp:claude-code', planned: { action: 'stop', target: 'sales-app', summary: 'Stop Sales app', destructive: false, changes: [{ field: 'state', from: 'Running', to: 'Stopped' }] } })

describe('Plan page', () => {
  it('shows who asked, the change, and approves it', async () => {
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans/p1': { body: fromAgent },
      'POST /api/plans/p1/approve': { body: { ...fromAgent, state: 'applied' } },
    })
    renderApp('/plans/p1')
    expect(await screen.findByText('Requested by an agent that calls itself claude-code')).toBeTruthy()
    const review = screen.getByLabelText('Stop Sales app')
    expect(within(review).getByLabelText('Changes').textContent).toContain('Stopped')
    await userEvent.click(within(review).getByRole('button', { name: 'Approve' }))
    await within(review).findByText('Done.')
    expect(calls.count('POST', '/api/plans/p1/approve')).toBe(1)
  })

  it('names the credential that asked, not only the name the agent gave itself', async () => {
    for (const [requestedBy, want] of [
      [{ token: 'laptop-cli' }, 'Requested with your API token laptop-cli, by an agent that calls itself claude-code'],
      [{ app: 'pi-agent' }, 'Requested with the token of your app pi-agent, by an agent that calls itself claude-code'],
      [{ job: true }, 'Requested with the token of one of your jobs, by an agent that calls itself claude-code'],
    ] as const) {
      mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans/p1': { body: { ...fromAgent, requestedBy } } })
      const { unmount } = renderApp('/plans/p1')
      expect(await screen.findByText(want)).toBeTruthy()
      unmount()
    }
  })

  it('rejects it', async () => {
    const calls = mockApi({
      'GET /api/me': { body: me },
      'GET /api/plans/p1': { body: fromAgent },
      'POST /api/plans/p1/reject': { body: { ...fromAgent, state: 'rejected' } },
    })
    renderApp('/plans/p1')
    await userEvent.click(await screen.findByRole('button', { name: 'Reject' }))
    expect(calls.count('POST', '/api/plans/p1/reject')).toBe(1)
  })

  it('shows an expired plan with no buttons', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans/p1': { body: { ...fromAgent, state: 'expired', message: AI_TEXT.planExpired } } })
    renderApp('/plans/p1')
    expect(await screen.findByText(AI_TEXT.planExpired)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
  })

  it("shows the server's words for someone else's plan", async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/plans/p1': { status: 404, body: { title: AI_TEXT.planNotFound } } })
    renderApp('/plans/p1')
    expect(await screen.findByText(AI_TEXT.planNotFound)).toBeTruthy()
  })
})
