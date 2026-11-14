import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FolderGrant, FoldersView } from '../../api'
import { mockApi, renderApp, type Reply } from '../../test/api'
import { allReady, me, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const never = [
  { path: '.ssh', why: 'holds your SSH keys' },
  { path: '.config/gh', why: 'holds your GitHub sign-in' },
]

const hidden = 'hidden files and folders hold settings and sign-ins'

const folders = (grants: FolderGrant[]): FoldersView => ({ project: '~/projects/sales-dashboard', grants, never, hidden })

const pi = (view: FoldersView | undefined) =>
  ws('pi', 'Pi', 'Running', allReady(), { template: 'pi', kind: 'pi', file: 'projects/sales-dashboard', apiAccess: 'None', folders: view })

function page(first: FoldersView | undefined, routes: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces/pi': { body: pi(first) },
    'PUT /api/workspaces/pi/folders': (req) => ({ body: pi(folders((req.body as { folders: FolderGrant[] }).folders)) }),
    ...routes,
  })
}

const HOME: Record<string, [string, 'folder' | 'file'][]> = {
  '': [['.config', 'folder'], ['.ssh', 'folder'], ['data', 'folder'], ['projects', 'folder'], ['.bashrc', 'file'], ['notes.md', 'file']],
  '.config': [['gh', 'folder'], ['ghostty', 'folder']],
  data: [['customers', 'folder'], ['sales-2026', 'folder']],
  projects: [['.github', 'folder'], ['marketing-site', 'folder'], ['sales-dashboard', 'folder'], ['shared-lib', 'folder']],
}

/** The answer of GET /api/files for the folder the request names. */
function listing(requested: string): Reply {
  const path = new URLSearchParams(requested.split('?')[1]).get('path') ?? ''
  const entries = (HOME[path] ?? []).map(([name, kind]) => ({ name, kind, size: 10, modified: '2026-11-02T10:00:00Z' }))
  return { body: { path, entries, count: entries.length, truncated: false, stale: false, ageSeconds: 0 } }
}

const card = async () => (await screen.findByRole('heading', { name: 'What Pi can open' })).closest('.card') as HTMLElement

describe('What a locked coding agent can open', () => {
  it('lists the project, which stays, and what was added, which can be removed', async () => {
    const api = page(folders([{ path: 'data/sales-2026', mode: 'read' }, { path: 'notes.md', mode: 'write' }]))
    renderApp('/apps/pi')
    const rows = within(await card()).getAllByRole('listitem')
    expect(rows.map((r) => r.textContent)).toEqual([
      expect.stringMatching(/~\/projects\/sales-dashboard.*This app's project.*Read and change/),
      expect.stringMatching(/~\/data\/sales-2026.*Added by you.*Read only/),
      expect.stringMatching(/~\/notes\.md.*Added by you.*Read and change/),
    ])
    expect(within(rows[0]).queryByRole('button')).toBeNull()

    await userEvent.click(within(rows[1]).getByRole('button', { name: 'Remove ~/data/sales-2026' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ folders: [{ path: 'notes.md', mode: 'write' }] }))
    await waitFor(async () => expect(within(await card()).getAllByRole('listitem')).toHaveLength(2))
  })

  it('is not on the page of an app that is not locked in', async () => {
    page(undefined)
    renderApp('/apps/pi')
    await screen.findByRole('heading', { name: 'Pi' })
    expect(screen.queryByRole('heading', { name: /can open/ })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Add a folder or file' })).toBeNull()
  })

  it('adds a folder to read only unless the person picks more, and says the agent restarts', async () => {
    const api = page(folders([{ path: 'data/sales-2026', mode: 'read' }]), { 'GET /api/files': (req) => listing(req.path) })
    renderApp('/apps/pi')
    await userEvent.click(within(await card()).getByRole('button', { name: 'Add a folder or file' }))
    const dialog = screen.getByRole('dialog', { name: 'Let Pi open another folder' })
    expect(within(dialog).getByText('Pick a folder or file in your home.')).toBeTruthy()
    // Nothing is picked yet, and the folder the list stands in is never picked by itself.
    expect(within(dialog).getByRole('button', { name: 'Pick a folder or file' }).hasAttribute('disabled')).toBe(true)
    expect(within(dialog).getByRole('radio', { name: 'Only read it' }).getAttribute('aria-checked')).toBe('true')
    const notice = within(dialog).getByRole('status', { name: 'Pi restarts to pick this up' })
    expect(notice.textContent).toContain('It takes a few seconds.')
    expect(dialog.textContent).not.toMatch(/chat|continues/i)

    await userEvent.click(await within(dialog).findByRole('button', { name: 'Open projects' }))
    await userEvent.click(await within(dialog).findByRole('button', { name: /^shared-lib/ }))
    await userEvent.click(within(dialog).getByRole('button', { name: 'Let Pi read ~/projects/shared-lib' }))
    await waitFor(() =>
      expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({
        folders: [{ path: 'data/sales-2026', mode: 'read' }, { path: 'projects/shared-lib', mode: 'read' }],
      }),
    )
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(within(await card()).getAllByRole('listitem').at(-1)?.textContent).toMatch(/~\/projects\/shared-lib.*Read only/)
  })

  it('names the wider choice on the button, and adds a file as well as a folder', async () => {
    const api = page(folders([]), { 'GET /api/files': (req) => listing(req.path) })
    renderApp('/apps/pi')
    await userEvent.click(within(await card()).getByRole('button', { name: 'Add a folder or file' }))
    const dialog = screen.getByRole('dialog', { name: 'Let Pi open another folder' })
    await userEvent.click(await within(dialog).findByRole('button', { name: /^notes\.md/ }))
    await userEvent.click(within(dialog).getByRole('radio', { name: 'Read and change it' }))
    await userEvent.click(within(dialog).getByRole('button', { name: 'Let Pi read and change ~/notes.md' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ folders: [{ path: 'notes.md', mode: 'write' }] }))
  })

  it('changes nothing when the dialog is cancelled', async () => {
    const api = page(folders([]), { 'GET /api/files': (req) => listing(req.path) })
    renderApp('/apps/pi')
    await userEvent.click(within(await card()).getByRole('button', { name: 'Add a folder or file' }))
    const dialog = screen.getByRole('dialog', { name: 'Let Pi open another folder' })
    await userEvent.click(await within(dialog).findByRole('button', { name: /^notes\.md/ }))
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(api.calls.some((c) => c.method === 'PUT')).toBe(false)
  })

  it('marks what no agent can open, and what is open already, with the reason, and does not let them be picked', async () => {
    const api = page(folders([{ path: 'data/sales-2026', mode: 'read' }]), { 'GET /api/files': (req) => listing(req.path) })
    renderApp('/apps/pi')
    await userEvent.click(within(await card()).getByRole('button', { name: 'Add a folder or file' }))
    const dialog = screen.getByRole('dialog', { name: 'Let Pi open another folder' })
    await userEvent.click(within(dialog).getByRole('switch', { name: 'Show hidden files' }))
    const row = (name: RegExp) => within(dialog).findByRole('button', { name })
    const home = () => within(within(dialog).getByRole('navigation', { name: 'Breadcrumbs' })).getByRole('button', { name: 'Home' })
    const refused = async (name: RegExp, why: string, red = true) => {
      const r = await row(name)
      expect(r.getAttribute('aria-disabled')).toBe('true')
      expect(r.className.split(' ').includes('no')).toBe(red)
      expect(r.textContent).toContain(why)
      await userEvent.click(r)
      expect(within(dialog).getByRole('button', { name: 'Pick a folder or file' }).hasAttribute('disabled')).toBe(true)
    }
    // What is never allowed, and why, is the API's to say: a reason of its own
    // for the places it lists, and one reason for everything else hidden at
    // the top of the home.
    await refused(/^\.ssh/, 'holds your SSH keys')
    await refused(/^\.config/, hidden)
    await refused(/^\.bashrc/, hidden)

    // Inside a hidden folder nothing can be picked either.
    await userEvent.click(within(dialog).getByRole('button', { name: 'Open .config' }))
    await refused(/^gh(?!ostty)/, 'holds your GitHub sign-in')
    await refused(/^ghostty/, hidden)

    await userEvent.click(home())
    await userEvent.click(await within(dialog).findByRole('button', { name: 'Open projects' }))
    await refused(/^sales-dashboard/, 'already open', false)
    // A hidden name further down is a project's own.
    await userEvent.click(await row(/^\.github/))
    expect(within(dialog).getByRole('button', { name: 'Let Pi read ~/projects/.github' }).hasAttribute('disabled')).toBe(false)
    await userEvent.click(home())
    await userEvent.click(await within(dialog).findByRole('button', { name: 'Open data' }))
    await refused(/^sales-2026/, 'already added', false)
    expect(api.calls.some((c) => c.method === 'PUT')).toBe(false)
  })

  it('keeps the dialog open with the reason when the server refuses the folder', async () => {
    page(folders([]), {
      'GET /api/files': (req) => listing(req.path),
      'PUT /api/workspaces/pi/folders': {
        status: 422,
        body: { title: 'Unprocessable Entity', errors: [{ location: 'body.folders', message: 'Not added. "notes.md": it is a link.' }] },
      },
    })
    renderApp('/apps/pi')
    await userEvent.click(within(await card()).getByRole('button', { name: 'Add a folder or file' }))
    const dialog = screen.getByRole('dialog', { name: 'Let Pi open another folder' })
    await userEvent.click(await within(dialog).findByRole('button', { name: /^notes\.md/ }))
    await userEvent.click(within(dialog).getByRole('button', { name: 'Let Pi read ~/notes.md' }))
    expect((await within(dialog).findByRole('alert')).textContent).toContain('it is a link')
    expect(screen.getByRole('dialog', { name: 'Let Pi open another folder' })).toBeTruthy()
  })
})
