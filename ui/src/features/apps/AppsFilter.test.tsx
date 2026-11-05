import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const shared = (n: number, over: object = {}) => ({
  name: `app-${n}`,
  displayName: `Shared app ${n}`,
  template: 'streamlit',
  kind: 'streamlit',
  phase: 'Running',
  url: `https://apps.example.com/u/bob/app-${n}/`,
  owner: { id: 'bob-bbbbbbbb', displayName: 'bob' },
  via: 'user',
  ...over,
})

function render(items: object[], mine: object[] = []) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces': { body: { items: mine } },
    'GET /api/shared': { body: { items, empty: 'Nothing has been shared with you yet.' } },
    'GET /api/templates': { body: { items: [] } },
  })
}

const mineApp = { name: 'sales-app', displayName: 'Sales app', template: 'streamlit', kind: 'streamlit', file: 'app.py', size: 'small', phase: 'Running', url: '/u/alice/sales-app/', steps: [] }

describe('All, Mine and Shared with me where apps are listed', () => {
  it('Apps page shows my apps and shared apps together by default', async () => {
    render([shared(1)], [mineApp])
    renderApp('/apps')
    expect(await screen.findByText('Sales app')).toBeTruthy()
    const tile = (await screen.findByText('Shared app 1')).closest('a') as HTMLElement
    expect(within(tile).getByText('from bob')).toBeTruthy()
    expect(tile.getAttribute('href')).toBe('https://apps.example.com/u/bob/app-1/')
    const filter = screen.getByRole('radiogroup', { name: 'Show' })
    expect(within(filter).getByRole('radio', { name: 'All' }).getAttribute('aria-checked')).toBe('true')
  })

  it.each(['Cards', 'List'])('All lists my apps and shared apps in one %s list, newest first; Mine leaves the shared ones out', async (view) => {
    const newer = { ...mineApp, name: 'newer', displayName: 'Newer app', createdAt: '2026-03-01T00:00:00Z' }
    const older = { ...mineApp, name: 'older', displayName: 'Older app', createdAt: '2026-01-01T00:00:00Z' }
    render([shared(1, { createdAt: '2026-02-01T00:00:00Z' })], [newer, older])
    renderApp('/apps')
    await screen.findByText('Shared app 1')
    await userEvent.click(screen.getByRole('button', { name: view }))
    const known = ['Newer app', 'Older app', 'Shared app 1']
    const names = () => screen.getAllByRole('link').flatMap((l) => known.filter((n) => l.textContent?.startsWith(n)))
    expect(names()).toEqual(['Newer app', 'Shared app 1', 'Older app'])
    if (view === 'List') {
      const rows = within(screen.getByRole('table', { name: 'Apps' })).getAllByRole('row')
      expect(rows).toHaveLength(4)
      const row = screen.getByRole('link', { name: 'Shared app 1' }).closest('tr') as HTMLElement
      expect(within(row).getByText(/from bob/)).toBeTruthy()
      expect(within(row).getByText('Shared with you')).toBeTruthy()
      expect(within(row).getByRole('link', { name: 'Open' }).getAttribute('href')).toBe('https://apps.example.com/u/bob/app-1/')
      expect(within(row).queryByRole('button')).toBeNull()
    }
    await userEvent.click(within(screen.getByRole('radiogroup', { name: 'Show' })).getByRole('radio', { name: 'Mine' }))
    expect(names()).toEqual(['Newer app', 'Older app'])
  })

  it('filters to my apps or to shared apps, and keeps the choice in the address', async () => {
    render([shared(1)], [mineApp])
    renderApp('/apps')
    const filter = await screen.findByRole('radiogroup', { name: 'Show' })
    await userEvent.click(within(filter).getByRole('radio', { name: 'Mine' }))
    expect(screen.getByText('Sales app')).toBeTruthy()
    expect(screen.queryByText('Shared app 1')).toBeNull()
    expect(location()).toBe('/apps?show=mine')
    await userEvent.click(within(filter).getByRole('radio', { name: 'Shared with me' }))
    expect(await screen.findByText('Shared app 1')).toBeTruthy()
    expect(screen.queryByText('Sales app')).toBeNull()
    expect(location()).toBe('/apps?show=shared')
    await userEvent.click(within(filter).getByRole('radio', { name: 'All' }))
    expect(location()).toBe('/apps')
  })

  it('opens on the shared filter from its address, and the old /shared address lands there', async () => {
    render([1, 2, 3, 4, 5, 6, 7].map((n) => shared(n)), [mineApp])
    renderApp('/shared')
    expect(await screen.findAllByText(/^Shared app/)).toHaveLength(7)
    expect(screen.queryByText('Sales app')).toBeNull()
    expect(location()).toBe('/apps?show=shared')
  })

  it('Home shows both by default with the same filter, and no separate shared section', async () => {
    render([shared(1)], [mineApp])
    renderApp('/')
    expect(await screen.findByText('Shared app 1')).toBeTruthy()
    expect(screen.getByText('Sales app')).toBeTruthy()
    expect(screen.getByRole('radiogroup', { name: 'Show' })).toBeTruthy()
    expect(screen.queryByRole('region', { name: 'Shared with you' })).toBeNull()
  })

  it.each(['/apps', '/'])('%s offers All, Mine and Shared with me even when nothing is shared', async (path) => {
    render([])
    renderApp(path)
    const filter = await screen.findByRole('radiogroup', { name: 'Show' })
    expect(within(filter).getAllByRole('radio').map((r) => r.textContent)).toEqual(['All', 'Mine', 'Shared with me'])
    await userEvent.click(within(filter).getByRole('radio', { name: 'Shared with me' }))
    expect(await screen.findByText('Nothing has been shared with you yet.')).toBeTruthy()
  })

  it('shows the API message when the shared list fails', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': { body: { items: [] } },
      'GET /api/shared': { status: 403, body: { title: 'Something went wrong on our side. Try again in a moment.' } },
    })
    renderApp('/apps?show=shared')
    expect((await screen.findByRole('alert')).textContent).toContain('Something went wrong on our side')
  })
})
