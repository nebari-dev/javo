import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const view = (over: Record<string, unknown> = {}) => ({
  userCode: 'WDJB-MJHT',
  createdAt: new Date().toISOString(),
  expiresAt: new Date(Date.now() + 600_000).toISOString(),
  tokenHours: 8,
  state: 'pending',
  ...over,
})

const ROUTE = 'GET /api/auth/device/WDJB-MJHT'

describe('Device login page', () => {
  it('shows the code, how long access lasts and what an agent can do', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText('WDJB-MJHT')).toBeTruthy()
    expect(screen.getByRole('heading', { level: 1, name: 'Approve an agent login' })).toBeTruthy()
    // The same card an approval in the inbox is: who asks, then the question.
    const card = screen.getByRole('article', { name: 'Let an agent act as you?' })
    expect(within(card).getByText('WDJB-MJHT')).toBeTruthy()
    expect(within(card).getByText('Check this matches the code in your terminal.')).toBeTruthy()
    expect(screen.getByText('Approving signs that device in as you through the API for 8 hours. Approve only if you started this yourself just now.')).toBeTruthy()
    const grants = screen.getByRole('list', { name: 'What the agent can do' })
    const rows = within(grants).getAllByRole('listitem').map((li) => [li.querySelector('b')?.textContent, li.querySelector('span')?.textContent])
    expect(rows).toEqual([
      ['Apps and jobs', 'Create, change, start, stop and delete them'],
      ['Files', 'Write in your home folder'],
      ['Secrets', 'Store and change them'],
      ['Sharing', 'Share your apps, including publicly'],
      ['API access', 'Give an app its own, which lasts until you turn it off'],
    ])
    expect(screen.getByText("It can't use admin powers, make or revoke tokens, or approve requests.")).toBeTruthy()
  })

  it('says where the request came from and what the device calls itself, as the server recorded them', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ askedFrom: '203.0.113.7', clientName: 'coding agent on Linux' }) } })
    renderApp('/device?code=WDJB-MJHT')
    const facts = await screen.findByRole('list', { name: 'Who is asking' })
    expect(within(facts).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Asked from 203.0.113.7',
      'It calls itself \u201ccoding agent on Linux\u201d. Anyone can choose this name.',
    ])
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('claims nothing the server did not record: no address, no name, no place', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ askedFrom: '', clientName: '' }) } })
    renderApp('/device?code=WDJB-MJHT')
    const card = await screen.findByRole('article', { name: 'Let an agent act as you?' })
    expect(within(card).queryByRole('list', { name: 'Who is asking' })).toBeNull()
    expect(card.textContent).not.toMatch(/on your computer|Asked from|calls itself/)
  })

  it('warns when the request came from another address than the browser', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ askedFrom: '203.0.113.7', fromAnotherAddress: true }) } })
    renderApp('/device?code=WDJB-MJHT')
    expect((await screen.findByRole('alert')).textContent).toBe(
      'This request did not come from the address your browser is on. If someone sent you this link, deny it.',
    )
  })

  it('says when the request was made', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ createdAt: new Date(Date.now() - 125_000).toISOString() }) } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText('Requested 2 minutes ago')).toBeTruthy()
  })

  it('says a request made under a minute ago was made just now', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText('Requested just now')).toBeTruthy()
  })

  it('says "1 hour" for a one hour token', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ tokenHours: 1 }) } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText(/through the API for 1 hour\./)).toBeTruthy()
  })

  it('says "less than an hour" when the token lasts under an hour', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ tokenHours: 0 }) } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText(/through the API for less than an hour\./)).toBeTruthy()
  })

  it('approves and says so', async () => {
    const api = mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() }, 'POST /api/auth/device/WDJB-MJHT/approve': { status: 204 } })
    renderApp('/device?code=WDJB-MJHT')
    await userEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    expect(await screen.findByText('Approved. You can go back to your agent.')).toBeTruthy()
    expect(api.count('POST', '/api/auth/device/WDJB-MJHT/approve')).toBe(1)
    expect(screen.queryByRole('button', { name: 'Deny' })).toBeNull()
  })

  it('denies and says so', async () => {
    const api = mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() }, 'POST /api/auth/device/WDJB-MJHT/deny': { status: 204 } })
    renderApp('/device?code=WDJB-MJHT')
    await userEvent.click(await screen.findByRole('button', { name: 'Deny' }))
    expect(await screen.findByText('Denied. The agent gets no access.')).toBeTruthy()
    expect(api.count('POST', '/api/auth/device/WDJB-MJHT/deny')).toBe(1)
  })

  it('says an expired request has expired', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/auth/device/XXXX-EXPD': { status: 410, body: { title: 'Gone' } } })
    renderApp('/device?code=XXXX-EXPD')
    expect(await screen.findByText('This request has expired. Ask your agent to start again.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
  })

  it('says an unknown code is unknown', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/auth/device/BBBB-CCCC': { status: 404, body: { title: 'Not found' } } })
    renderApp('/device?code=BBBB-CCCC')
    expect(await screen.findByText('No request has this code. Check it and try again.')).toBeTruthy()
  })

  it('asks for the code when none is in the address, and opens it', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() } })
    renderApp('/device')
    await userEvent.type(await screen.findByLabelText('Code shown by your agent'), 'WDJB-MJHT')
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(location()).toBe('/device?code=WDJB-MJHT')
    expect(await screen.findByRole('button', { name: 'Approve' })).toBeTruthy()
  })

  it.each(['WDJBMJHT', 'wdjb mjht', ' wdjb-mjht '])('opens the request for the code typed as %j', async (typed) => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() } })
    renderApp('/device')
    await userEvent.type(await screen.findByLabelText('Code shown by your agent'), typed)
    await userEvent.click(screen.getByRole('button', { name: 'Continue' }))
    expect(location()).toBe('/device?code=WDJB-MJHT')
  })

  it('says what was handed over: the same power the person has as a member, as a warning', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText('This gives the agent the same power you have as a member.')).toBeTruthy()
    const grants = screen.getByRole('list', { name: 'What the agent can do' })
    // Plain lines, never a safe-looking tick.
    expect(grants.querySelector('.ok')).toBeNull()
    expect(screen.getByText('This gives the agent the same power you have as a member.').closest('.warn')).toBeTruthy()
  })

  it('says a request that was already decided was already answered', async () => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view({ state: 'approved' }) } })
    renderApp('/device?code=WDJB-MJHT')
    expect(await screen.findByText('This request was already answered.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Approve' })).toBeNull()
  })

  it.each([
    [404, 'No request has this code. Check it and try again.'],
    [410, 'This request has expired. Ask your agent to start again.'],
    [409, 'This request was already answered.'],
  ])('shows the same words when Approve answers %i', async (status, text) => {
    mockApi({ 'GET /api/me': { body: me }, [ROUTE]: { body: view() }, 'POST /api/auth/device/WDJB-MJHT/approve': { status, body: { title: 'x' } } })
    renderApp('/device?code=WDJB-MJHT')
    await userEvent.click(await screen.findByRole('button', { name: 'Approve' }))
    expect((await screen.findByRole('alert')).textContent).toBe(text)
  })

  it('returns to the same address after signing in', async () => {
    mockApi({ 'GET /api/me': { status: 401, body: { title: 'Unauthorized' } }, [ROUTE]: { status: 401, body: { title: 'Unauthorized' } } })
    renderApp('/device?code=WDJB-MJHT')
    await waitFor(() => expect(location()).toBe('/login?next=' + encodeURIComponent('/device?code=WDJB-MJHT')))
  })
})
