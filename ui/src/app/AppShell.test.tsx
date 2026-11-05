import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router'
import { describe, expect, it } from 'vitest'
import { applyBrand } from '../lib/brand'
import { AppShell } from './AppShell'
import { makeQueryClient } from '../lib/queries'
import { mockApi } from '../test/api'
import { me, storageView } from '../test/fixtures'

const sha = '0123456789abcdef0123456789abcdef01234567'

function setup(path = '/', who: object = me, info: object = { apiDocs: false, version: 'v0.2.22', commit: sha }) {
  mockApi({ 'GET /api/me': { body: who }, 'GET /api/storage': { body: storageView() }, 'GET /api/info': { body: info } })
  return render(
    <QueryClientProvider client={makeQueryClient()}>
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<AppShell />}>
          <Route index element={<p>Home placeholder</p>} />
          <Route path="apps/new" element={<p>New app placeholder</p>} />
          <Route path="apps" element={<p>Apps placeholder</p>} />
          <Route path="shared" element={<p>Shared placeholder</p>} />
          <Route path="settings" element={<p>Settings placeholder</p>} />
        </Route>
      </Routes>
    </MemoryRouter>
    </QueryClientProvider>,
  )
}

describe('AppShell', () => {
  it('renders the routed page, navigation and theme switch', () => {
    setup()
    expect(screen.getByText('Home placeholder')).toBeTruthy()
    const nav = screen.getByRole('navigation', { name: 'Main' })
    expect(within(nav).getByRole('link', { name: 'Home' })).toBeTruthy()
    expect(screen.getByRole('button', { name: /^Appearance/ })).toBeTruthy()
  })

  it('goes to the new app route from the primary button', async () => {
    setup()
    await userEvent.click(screen.getByRole('button', { name: 'New app' }))
    expect(screen.getByText('New app placeholder')).toBeTruthy()
  })

  it('links to the apps list from the navigation', async () => {
    setup()
    await userEvent.click(screen.getByRole('link', { name: 'Apps' }))
    expect(screen.getByText('Apps placeholder')).toBeTruthy()
  })

  it('collapses the sidebar and remembers it across mounts', async () => {
    const first = setup()
    const toggle = screen.getByRole('button', { name: 'Collapse sidebar' })
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    await userEvent.click(toggle)
    expect(screen.getByRole('button', { name: 'Expand sidebar' }).getAttribute('aria-expanded')).toBe('false')
    first.unmount()

    setup()
    expect(screen.getByRole('button', { name: 'Expand sidebar' })).toBeTruthy()
  })

  it('keeps nav links reachable by name when collapsed', async () => {
    setup()
    await userEvent.click(screen.getByRole('button', { name: 'Collapse sidebar' }))
    expect(screen.getByRole('link', { name: 'Home' })).toBeTruthy()
  })

  it('names a local account, which has no display name, by its username', async () => {
    mockApi({ 'GET /api/me': { body: { ...me, displayName: '' } } })
    render(
      <QueryClientProvider client={makeQueryClient()}>
        <MemoryRouter>
          <Routes>
            <Route element={<AppShell />}>
              <Route index element={<p>Home placeholder</p>} />
            </Route>
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    expect(await screen.findByRole('button', { name: 'Account: alice' })).toBeTruthy()
  })

  it('has one sign-out control, in the account menu', async () => {
    setup()
    expect(screen.queryByRole('button', { name: 'Sign out' })).toBeNull()
    await userEvent.click(await screen.findByRole('button', { name: 'Account: Alice' }))
    expect(screen.getAllByRole('menuitem', { name: 'Sign out' })).toHaveLength(1)
  })

  it('has no separate sidebar entry for shared apps', () => {
    setup()
    expect(screen.queryByRole('link', { name: 'Shared with me' })).toBeNull()
  })

  it('opens settings from the foot of the sidebar', async () => {
    setup()
    const foot = screen.getByRole('navigation', { name: 'Settings and help' })
    await userEvent.click(within(foot).getByRole('link', { name: 'Settings' }))
    expect(screen.getByText('Settings placeholder')).toBeTruthy()
  })

  it('shows the running release above the Settings section, linking to its release page', async () => {
    setup()
    const foot = screen.getByRole('navigation', { name: 'Settings and help' })
    const version = await screen.findByRole('link', { name: 'v0.2.22' })
    expect(version.getAttribute('href')).toBe('https://github.com/nebari-dev/javo/releases/tag/v0.2.22')
    expect(foot.contains(version)).toBe(false)
    expect(version.compareDocumentPosition(foot) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('shows a dev build by its short commit, linking to the commit', async () => {
    setup('/', me, { apiDocs: false, version: 'dev', commit: sha })
    const version = await screen.findByRole('link', { name: 'dev+0123456' })
    expect(version.getAttribute('href')).toBe(`https://github.com/nebari-dev/javo/commit/${sha}`)
  })

  it('goes home from the logo and product name', async () => {
    setup('/apps')
    await userEvent.click(screen.getByRole('link', { name: 'Javo home' }))
    expect(screen.getByText('Home placeholder')).toBeTruthy()
  })

  it('opens the navigation from the menu button on small screens and closes it after a choice', async () => {
    setup('/')
    const menu = screen.getByRole('button', { name: 'Open menu' })
    expect(menu.getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(menu)
    expect(menu.getAttribute('aria-expanded')).toBe('true')
    await userEvent.click(screen.getByRole('link', { name: 'Apps' }))
    expect(screen.getByText('Apps placeholder')).toBeTruthy()
    expect(menu.getAttribute('aria-expanded')).toBe('false')
  })

  it('closes the menu on Escape and on the backdrop', async () => {
    setup('/')
    const menu = screen.getByRole('button', { name: 'Open menu' })
    await userEvent.click(menu)
    await userEvent.keyboard('{Escape}')
    expect(menu.getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(menu)
    await userEvent.click(screen.getByTestId('nav-backdrop'))
    expect(menu.getAttribute('aria-expanded')).toBe('false')
  })

  it('shows no storage note or storage item in the sidebar, for admins either', async () => {
    setup('/', { ...me, roles: ['platform-admin'] })
    await screen.findByRole('button', { name: /^Account:/ })
    await new Promise((r) => setTimeout(r, 20))
    expect(screen.queryByText('Files on one machine')).toBeNull()
    expect(screen.queryByRole('link', { name: 'Storage' })).toBeNull()
  })

  it('shows the Javo name and mark without branding', () => {
    setup()
    const home = screen.getByRole('link', { name: 'Javo home' })
    expect(within(home).getByText('Javo')).toBeTruthy()
    expect(home.querySelector('svg.brand-mark')).toBeTruthy()
    expect(home.querySelector('img')).toBeNull()
  })

  it('shows the configured name and logo in the sidebar', () => {
    applyBrand({ name: 'Acme Lab', logoLight: '/branding/logo-light?v=1', logoDark: 'https://cdn.example.com/dark.svg' })
    setup()
    const home = screen.getByRole('link', { name: 'Acme Lab home' })
    expect(within(home).getByText('Acme Lab')).toBeTruthy()
    expect(home.querySelector('svg.brand-mark')).toBeNull()
    const srcs = [...home.querySelectorAll('img')].map((i) => i.getAttribute('src'))
    expect(srcs).toEqual(['/branding/logo-light?v=1', 'https://cdn.example.com/dark.svg'])
  })
})
