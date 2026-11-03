import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../test/api'
import { me, templates } from '../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

function base() {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items: templates } },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/sizes': { body: { items: [] } },
    'GET /api/jobs': { body: { items: [] } },
    'GET /api/engines': { body: { items: [] } },
    'POST /api/files/warm': { status: 204 },
  })
}

describe('warming the home folder ahead of the file picker', () => {
  it('asks once the user is signed in, and again on opening New app', async () => {
    const api = base()
    renderApp('/apps')
    await waitFor(() => expect(api.count('POST', '/api/files/warm')).toBe(1))
    await userEvent.click(await screen.findByRole('button', { name: 'New app' }))
    expect(location()).toBe('/apps/new')
    await waitFor(() => expect(api.count('POST', '/api/files/warm')).toBe(2))
  })

  it('asks on opening New job', async () => {
    const api = base()
    renderApp('/jobs/new')
    // One for the sign-in, one for the page.
    await waitFor(() => expect(api.count('POST', '/api/files/warm')).toBe(2))
  })

  it('is a hint: a failed warm-up shows nothing', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/workspaces': { body: { items: [] } },
      'GET /api/sizes': { body: { items: [] } },
      'POST /api/files/warm': { status: 500, body: { title: 'Something went wrong' } },
    })
    renderApp('/apps/new')
    await waitFor(() => expect(api.count('POST', '/api/files/warm')).toBeGreaterThan(0))
    expect(screen.queryByText('Something went wrong')).toBeNull()
  })
})
