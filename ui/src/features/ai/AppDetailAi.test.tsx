import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { AI_TEXT, aiOff, aiOn, diagnosis, me, plan, stepsAt, ws } from '../../test/fixtures'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const crashed = ws('sales-app', 'Sales app', 'Failed', stepsAt(3, 'AppCrashed', ['container app terminated: Error, exit code 1']))

const explanation = {
  explanation: {
    summary: 'The app imports plotly, which is not installed. <img src=x> [link](http://x)',
    cause: 'plotly is missing.',
    confidence: 'high',
    evidence: ['log/app/prev/41'],
    fix: 'Add plotly to the environment.',
    actions: [{ action: 'restart', target: 'sales-app' }],
  },
  bundle: { text: '<untrusted ref="condition/Started">x</untrusted>', refs: ['condition/Started'], hidden: 0, withheld: 2, notes: [AI_TEXT.withheld] },
  model: 'claude-opus-5-5',
  dropped: { refs: 0, actions: 0 },
  proposals: [{ action: 'restart', target: 'sales-app', summary: 'Restart Sales app' }],
}

function api(over: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces/sales-app': { body: crashed },
    'GET /api/workspaces/sales-app/diagnosis': { body: diagnosis() },
    'GET /api/workspaces/sales-app/events': { body: { items: [] } },
    'GET /api/workspaces/sales-app/logs': { body: { lines: [], restarts: 0 } },
    'GET /api/ai/status': { body: aiOn },
    'POST /api/workspaces/sales-app/diagnosis/explain': { body: explanation },
    'POST /api/ai/feedback': { status: 204 },
    'POST /api/plans': { status: 201, body: plan() },
    'POST /api/plans/p1/approve': { body: plan({ state: 'applied' }) },
    'POST /api/plans/p1/reject': { body: plan({ state: 'rejected' }) },
    ...over,
  })
}

describe('Failure diagnosis', () => {
  it('shows the rules finding and its fix with AI off, and no AI controls', async () => {
    api({ 'GET /api/ai/status': { body: aiOff } })
    renderApp('/apps/sales-app')
    const card = await screen.findByRole('alert')
    await within(card).findByText(AI_TEXT.MissingModule.title)
    expect(within(card).getByText(AI_TEXT.MissingModule.todo)).toBeTruthy()
    expect(within(card).getByRole('button', { name: 'Restart Sales app' })).toBeTruthy()
    await userEvent.click(within(card).getByRole('button', { name: /Who can help/ }))
    expect(within(card).getByText(/No module named 'plotly'/)).toBeTruthy()
    expect(screen.queryByText(/Explain more/)).toBeNull()
    expect(screen.queryByText(/AI/)).toBeNull()
  })

  it('shows a fix for review first and applies it only on Apply', async () => {
    const calls = api()
    renderApp('/apps/sales-app')
    const card = await screen.findByRole('alert')
    await userEvent.click(await within(card).findByRole('button', { name: 'Restart Sales app' }))
    const review = await within(card).findByLabelText('Restart Sales app')
    expect(within(review).getByLabelText('Changes').textContent).toContain('Restarted')
    expect(calls.count('POST', '/api/plans/p1/approve')).toBe(0)
    expect(calls.calls.find((c) => c.path === '/api/plans')?.body).toEqual({ proposal: { action: 'restart', target: 'sales-app' }, source: 'rules' })
    await userEvent.click(within(review).getByRole('button', { name: 'Apply' }))
    await within(review).findByText('Done.')
    expect(calls.count('POST', '/api/plans/p1/approve')).toBe(1)
  })

  it('shows the catalog text when the change was already made', async () => {
    api({ 'POST /api/plans/p1/approve': { status: 409, body: { title: AI_TEXT.planDone } } })
    renderApp('/apps/sales-app')
    const card = await screen.findByRole('alert')
    await userEvent.click(await within(card).findByRole('button', { name: 'Restart Sales app' }))
    await userEvent.click(await within(card).findByRole('button', { name: 'Apply' }))
    expect(await within(card).findByText(AI_TEXT.planDone)).toBeTruthy()
  })

  it('offers Explain more, collapsed, when the rules found the cause', async () => {
    const calls = api()
    renderApp('/apps/sales-app')
    const ai = await screen.findByLabelText('AI explanation')
    expect(within(ai).getByRole('button', { name: 'Explain more' })).toBeTruthy()
    expect(calls.count('POST', '/api/workspaces/sales-app/diagnosis/explain')).toBe(0)
  })

  it('asks at once, and renders the answer as plain text, when the cause is unknown', async () => {
    const calls = api({ 'GET /api/workspaces/sales-app/diagnosis': { body: diagnosis({ code: 'AppCrashed', known: false, rule: 'from-condition' }) } })
    const { container } = renderApp('/apps/sales-app')
    const ai = await screen.findByLabelText('AI explanation')
    await within(ai).findByText(/which is not installed/)
    expect(within(ai).getByText(/<img src=x> \[link\]\(http:\/\/x\)/)).toBeTruthy()
    expect(container.querySelector('.ai-card img, .ai-card a')).toBeNull()
    expect(within(ai).getByText('claude-opus-5-5')).toBeTruthy()
    expect(within(ai).getByText('Based on the evidence above. Any change is shown for review first.')).toBeTruthy()
    const ref = within(ai).getByRole('button', { name: 'log/app/prev/41' })
    await userEvent.click(ref)
    expect(document.activeElement?.id).toBe('ev-log-app-prev-41')
    expect(calls.count('POST', '/api/workspaces/sales-app/diagnosis/explain')).toBe(1)
    await userEvent.click(within(ai).getByRole('button', { name: 'No' }))
    await within(ai).findByText('Thanks.')
    expect(calls.calls.find((c) => c.path === '/api/ai/feedback')?.body).toEqual({ feature: 'explain', ref: 'sales-app', right: false })
  })

  it('shows what will be sent, verbatim, behind More', async () => {
    api({ 'GET /api/workspaces/sales-app/diagnosis': { body: diagnosis({ known: false }) } })
    renderApp('/apps/sales-app')
    const ai = await screen.findByLabelText('AI explanation')
    await userEvent.click(within(ai).getByRole('button', { name: 'What will be sent' }))
    expect(await within(ai).findByText('<untrusted ref="condition/Started">x</untrusted>')).toBeTruthy()
    expect(within(ai).getByText(AI_TEXT.withheld)).toBeTruthy()
  })

  it('waits out a busy model and asks again; the rules finding stays', async () => {
    const busy = { ...explanation, explanation: undefined, proposals: [], message: AI_TEXT.busy, retryAfter: 1 }
    const calls = api({
      'GET /api/workspaces/sales-app/diagnosis': { body: diagnosis({ known: false }) },
      'POST /api/workspaces/sales-app/diagnosis/explain': [{ body: busy }, { body: explanation }],
    })
    renderApp('/apps/sales-app')
    const ai = await screen.findByLabelText('AI explanation')
    await within(ai).findByText(AI_TEXT.busy)
    expect(screen.getByRole('alert')).toBeTruthy()
    await within(ai).findByText(/which is not installed/, undefined, { timeout: 3000 })
    expect(calls.count('POST', '/api/workspaces/sales-app/diagnosis/explain')).toBe(2)
  })

  it('offers Retry when the model cannot be reached', async () => {
    const down = { ...explanation, explanation: undefined, proposals: [], message: AI_TEXT.unavailable }
    const calls = api({
      'GET /api/workspaces/sales-app/diagnosis': { body: diagnosis({ known: false }) },
      'POST /api/workspaces/sales-app/diagnosis/explain': [{ body: down }, { body: explanation }],
    })
    renderApp('/apps/sales-app')
    const ai = await screen.findByLabelText('AI explanation')
    await within(ai).findByText(AI_TEXT.unavailable)
    await userEvent.click(within(ai).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(calls.count('POST', '/api/workspaces/sales-app/diagnosis/explain')).toBe(2))
  })
})
