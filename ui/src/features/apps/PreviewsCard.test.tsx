import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderIn } from '../../test/api'
import { LIMIT_TEXT, previewApp, repoApp } from '../../test/fixtures'
import { isGitHubRepo, PreviewsCard } from './PreviewsCard'

afterEach(() => vi.unstubAllGlobals())

const WARNING = 'A pull request from a fork runs code you have not reviewed.'
const off = { enabled: false, max: 3, maxAllowed: 10, forks: false, forksWarning: WARNING, items: [] }
const on = {
  enabled: true,
  max: 3,
  maxAllowed: 10,
  forks: false,
  forksWarning: WARNING,
  syncedAt: new Date(Date.now() - 5 * 60_000).toISOString(),
  items: [
    { number: 12, title: 'Add a chart', app: 'site--pr-12' },
    { number: 14, title: 'Fix the footer', app: 'site--pr-14' },
    { number: 15, title: 'Dark mode', app: 'site--pr-15' },
    { number: 9, title: 'Old idea', reason: 'PreviewLimitReached', text: LIMIT_TEXT },
  ],
}
const children = { items: [previewApp('site', 12), previewApp('site', 14, 'Starting'), previewApp('site', 15, 'Failed')] }

function api(previews: object, extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/workspaces/site/previews': { body: previews },
    'GET /api/workspaces': { body: children },
    ...extra,
  })
}

describe('PreviewsCard', () => {
  it('starts off and turns previews on with the default limit', async () => {
    const m = api(off, { 'PUT /api/workspaces/site/previews': { body: { ...off, enabled: true } } })
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    const sw = await screen.findByRole('switch', { name: 'Preview open pull requests' })
    expect(sw.getAttribute('aria-checked')).toBe('false')
    expect(screen.queryByRole('list', { name: 'Pull requests' })).toBeNull()
    await userEvent.click(sw)
    await waitFor(() => expect(m.calls.find((c) => c.method === 'PUT')?.body).toEqual({ enabled: true, max: 3, forks: false }))
  })

  it('lists a link per preview with its phase, and the reason of a pull request without one', async () => {
    api(on)
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    const list = await screen.findByRole('list', { name: 'Pull requests' })
    await within(list).findByRole('link', { name: /#12 Add a chart/ })
    const links = within(list).getAllByRole('link', { name: /^#\d+/ })
    expect(links.map((l) => l.getAttribute('href'))).toEqual(['/apps/site--pr-12', '/apps/site--pr-14', '/apps/site--pr-15'])
    expect(within(list).getByText('Running')).toBeTruthy()
    expect(within(list).getByText('Starting')).toBeTruthy()
    expect(within(list).getByText('Failed')).toBeTruthy()
    expect(within(list).getByText(/#9 Old idea/)).toBeTruthy()
    expect(within(list).queryByRole('link', { name: /#9/ })).toBeNull()
    expect(within(list).getByText(LIMIT_TEXT)).toBeTruthy()
    expect(screen.getByText(/Checked 5 minutes ago/)).toBeTruthy()
  })

  it('says when no pull request is open', async () => {
    api({ ...on, items: [] })
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    expect(await screen.findByText('No open pull requests.')).toBeTruthy()
  })

  it('shows the words of a failed look at GitHub', async () => {
    api({
      ...on,
      reason: 'PreviewRateLimited',
      title: 'GitHub request limit reached',
      text: 'GitHub allows only so many requests an hour. Try again at 10:00.',
      todo: 'Nothing to do. Previews catch up after the time in the message.',
    })
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    expect(await screen.findByText(/Try again at 10:00/)).toBeTruthy()
    expect(screen.getByText('GitHub request limit reached')).toBeTruthy()
    expect(screen.getByText('Nothing to do. Previews catch up after the time in the message.')).toBeTruthy()
  })

  it('keeps limit, forks and webhook behind a disclosure that names them', async () => {
    api(on)
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    await screen.findByRole('list', { name: 'Pull requests' })
    expect(screen.queryByLabelText('Previews at most')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Limit, pull requests from forks and webhook' }))
    expect(screen.getByLabelText('Previews at most')).toBeTruthy()
    expect(screen.getByText(WARNING)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Hide limit, forks and webhook' })).toBeTruthy()
  })

  it('offers only the limits the platform allows', async () => {
    api({ ...on, maxAllowed: 4 })
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    await screen.findByRole('list', { name: 'Pull requests' })
    await userEvent.click(screen.getByRole('button', { name: 'Limit, pull requests from forks and webhook' }))
    const options = within(screen.getByLabelText('Previews at most')).getAllByRole('option')
    expect(options.map((o) => o.textContent)).toEqual(['1', '2', '3', '4'])
  })

  it('saves a new limit and the forks switch', async () => {
    const m = api(on, { 'PUT /api/workspaces/site/previews': { body: on } })
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    await screen.findByRole('list', { name: 'Pull requests' })
    await userEvent.click(screen.getByRole('button', { name: 'Limit, pull requests from forks and webhook' }))
    await userEvent.selectOptions(screen.getByLabelText('Previews at most'), '5')
    await waitFor(() => expect(m.calls.filter((c) => c.method === 'PUT').at(-1)?.body).toEqual({ enabled: true, max: 5, forks: false }))
    await userEvent.click(screen.getByRole('switch', { name: 'Also preview pull requests from forks' }))
    await waitFor(() => expect(m.calls.filter((c) => c.method === 'PUT').at(-1)?.body).toEqual({ enabled: true, max: 3, forks: true }))
  })

  it('shows the webhook address and secret once, after Set up', async () => {
    api(on, {
      'POST /api/workspaces/site/previews/webhook': {
        body: { url: 'https://apps.example.com/hooks/github/abc', secret: 'secret-value-123', notice: 'Copy the secret now. It is shown once and cannot be shown again.' },
      },
    })
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    await screen.findByRole('list', { name: 'Pull requests' })
    await userEvent.click(screen.getByRole('button', { name: 'Limit, pull requests from forks and webhook' }))
    expect(screen.queryByText('secret-value-123')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Set up webhook' }))
    expect(await screen.findByText('secret-value-123')).toBeTruthy()
    expect(screen.getByText('https://apps.example.com/hooks/github/abc')).toBeTruthy()
    expect(screen.getByText(/Copy the secret now/)).toBeTruthy()
  })

  it('says a webhook is set up, and warns that a new one stops the old secret before replacing it', async () => {
    const app = repoApp('site')
    const configured = { ...app, source: { ...app.source!, webhookConfigured: true } }
    const hookApi = api(on, {
      'POST /api/workspaces/site/previews/webhook': { body: { url: 'https://apps.example.com/hooks/github/abc', secret: 'secret-two-456', notice: 'Copy the secret now.' } },
    })
    renderIn(<PreviewsCard ws={configured} />)
    await screen.findByRole('list', { name: 'Pull requests' })
    await userEvent.click(screen.getByRole('button', { name: 'Limit, pull requests from forks and webhook' }))
    expect(screen.getByText('Webhook: set up')).toBeTruthy()
    expect(screen.getByText(/stops working/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Set up webhook' })).toBeNull()
    expect(hookApi.count('POST', '/api/workspaces/site/previews/webhook')).toBe(0)
    await userEvent.click(screen.getByRole('button', { name: 'Replace webhook address and secret' }))
    expect(await screen.findByText('secret-two-456')).toBeTruthy()
  })

  it('says no webhook is set up before the first one', async () => {
    api(on)
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    await screen.findByRole('list', { name: 'Pull requests' })
    await userEvent.click(screen.getByRole('button', { name: 'Limit, pull requests from forks and webhook' }))
    expect(screen.getByText('Webhook: not set up')).toBeTruthy()
  })

  it('shows the API text when the settings cannot be read', async () => {
    mockApi({ 'GET /api/workspaces/site/previews': { status: 404, body: { title: 'There is nothing at this address.' } }, 'GET /api/workspaces': { body: children } })
    renderIn(<PreviewsCard ws={repoApp('site')} />)
    expect(await screen.findByText('There is nothing at this address.')).toBeTruthy()
  })
})

describe('isGitHubRepo', () => {
  it('takes what the API takes as a GitHub repository', () => {
    for (const ok of ['https://github.com/example-org/site', 'https://GitHub.com/example-org/site.git/', ' https://github.com/a/b.c ']) {
      expect(isGitHubRepo(ok), ok).toBe(true)
    }
    for (const no of ['', 'http://github.com/example-org/site', 'https://github.com/example-org', 'https://github.com/example-org/site/tree/main', 'https://github.com:8443/a/b', 'https://git.example.org/a/b', 'https://github.com/-a/b', 'https://github.com/a/..']) {
      expect(isGitHubRepo(no), no).toBe(false)
    }
  })
})
