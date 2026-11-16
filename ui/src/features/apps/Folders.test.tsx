import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { FolderGrant, FoldersView, LockPlace } from '../../api'
import { mockApi, renderApp, type Reply } from '../../test/api'
import { allReady, me, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

// A few of the places the API sends, one of each rule.
const places: LockPlace[] = [
  { path: '.ssh', rule: 'risky', what: 'your SSH keys', means: 'Anyone who has these keys can sign in to your servers and push code as you.' },
  { path: '.config/gh', rule: 'risky', what: 'your GitHub sign-in', means: 'Anyone who has it can act as you on GitHub.' },
  { path: '.claude', rule: 'open', mode: 'write', what: "Claude Code's own settings" },
  { path: '.gitconfig', rule: 'open', mode: 'read', what: "Git's settings" },
  { path: '.javo', rule: 'closed', what: "the platform's own files", means: 'They belong to the platform, not to an agent.' },
  { path: '.config/javo', rule: 'closed', what: 'your sign-in to this platform', means: 'An agent that has it can lift its own lock.' },
  {
    path: '.config',
    rule: 'around',
    what: 'your sign-in to this platform, in ~/.config/javo',
    means: 'An agent that has it can lift its own lock. To open something else in ~/.config, tick it there, not the whole folder.',
  },
]
const hidden: LockPlace = { path: '.', rule: 'risky', what: "a tool's settings or sign-ins", means: 'Anyone who has a sign-in kept there can act as you with that tool.' }

const folders = (grants: FolderGrant[], project = '~/projects/sales-dashboard'): FoldersView => ({ project, grants, places, hidden })

const agent = (view: FoldersView | undefined, kind = 'claude-code') =>
  ws('cc', 'Sales agent', 'Running', allReady(), { template: kind, kind, file: 'projects/sales-dashboard', apiAccess: 'None', folders: view })

const HOME: Record<string, [string, 'folder' | 'file'][]> = {
  '': [
    ['.claude', 'folder'], ['.config', 'folder'], ['.javo', 'folder'], ['.ssh', 'folder'],
    ['Documents', 'folder'], ['data', 'folder'], ['notebooks', 'folder'], ['projects', 'folder'],
    ['.gitconfig', 'file'], ['.npmrc', 'file'], ['notes.md', 'file'],
  ],
  '.config': [['gh', 'folder'], ['ghostty', 'folder'], ['javo', 'folder']],
  '.ssh': [['known_hosts', 'file']],
  data: [['customers', 'folder'], ['sales-2026', 'folder']],
  'data/sales-2026': [['q1.csv', 'file'], ['q2.csv', 'file']],
  projects: [['.github', 'folder'], ['marketing-site', 'folder'], ['sales-dashboard', 'folder'], ['shared-lib', 'folder']],
  'projects/sales-dashboard': [['app.py', 'file']],
}

/** The answer of GET /api/files for the folder the request names. */
function listing(requested: string): Reply {
  const path = new URLSearchParams(requested.split('?')[1]).get('path') ?? ''
  if (!(path in HOME) && !path.includes('/')) return { status: 404, body: { title: 'That folder does not exist' } }
  const entries = (HOME[path] ?? []).map(([name, kind]) => ({ name, kind, size: 10, modified: '2026-11-02T10:00:00Z' }))
  return { body: { path, entries, count: entries.length, truncated: false, stale: false, ageSeconds: 0 } }
}

function page(first: FoldersView | undefined, routes: Parameters<typeof mockApi>[0] = {}, kind = 'claude-code') {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces/cc': { body: agent(first, kind) },
    'GET /api/files': (req) => listing(req.path),
    'PUT /api/workspaces/cc/folders': (req) => ({ body: agent(folders((req.body as { folders: FolderGrant[] }).folders), kind) }),
    ...routes,
  })
}

const tree = () => screen.findByRole('tree', { name: 'Your home' })
const row = async (name: string) => within(await tree()).findByRole('treeitem', { name })
const tick = (r: HTMLElement) => within(r).getByRole('checkbox') as HTMLInputElement
const showHidden = async () => userEvent.click(await screen.findByRole('switch', { name: 'Show hidden files and folders' }))
const card = async () => (await screen.findByRole('heading', { name: /^Which files and folders can .* access\?$/ })).closest('.card') as HTMLElement
const sent = (api: ReturnType<typeof mockApi>) => api.calls.find((c) => c.method === 'PUT')?.body
/** The lines under "<agent> can" or "<agent> cannot". */
const lines = (which: 'can' | 'cannot') =>
  within(screen.getByRole('heading', { name: `Claude Code ${which}` }).parentElement as HTMLElement)
    .getAllByRole('listitem')
    .map((li) => li.textContent)
const open = async (name: string) => userEvent.click(within(await row(name)).getByRole('button', { name: `Expand ${name}` }))

describe('What a locked coding agent can open', () => {
  it('shows the home as a tree, with the project and what was added ticked and everything else not', async () => {
    page(folders([{ path: 'data/sales-2026', mode: 'read' }]))
    renderApp('/apps/cc')
    expect(await screen.findByRole('heading', { name: 'Which files and folders can Claude Code access?' })).toBeTruthy()

    // The folders around the project and around what was added are open already.
    const project = await row('sales-dashboard')
    expect(project.textContent).toContain("This app's project. Claude Code can always read and change it")
    expect(project.textContent).toContain('Read and change')
    expect(tick(project).checked).toBe(true)
    expect(tick(project).getAttribute('aria-disabled')).toBe('true')

    const added = await row('sales-2026')
    expect(tick(added).checked).toBe(true)
    expect(within(added).getByRole('radio', { name: 'Read only' }).getAttribute('aria-checked')).toBe('true')

    const closed = await row('notebooks')
    expect(tick(closed).checked).toBe(false)
    expect(within(closed).queryByRole('radio')).toBeNull()
    expect(tick(await row('notes.md')).checked).toBe(false)
    // Children load when a folder opens, not before.
    expect(within(await tree()).queryByRole('treeitem', { name: 'q1.csv' })).toBeNull()
    await open('sales-2026')
    expect(await row('q1.csv')).toBeTruthy()
  })

  it('is not on the page of an app that is not locked in', async () => {
    page(undefined)
    renderApp('/apps/cc')
    await screen.findByRole('heading', { name: 'Sales agent' })
    expect(screen.queryByRole('heading', { name: /can .* access/ })).toBeNull()
    expect(screen.queryByRole('tree')).toBeNull()
  })

  it('collects what is ticked and sends the whole list with one Save, which says the agent restarts', async () => {
    const api = page(folders([{ path: 'data/sales-2026', mode: 'read' }]))
    renderApp('/apps/cc')
    const c = await card()
    await row('notebooks')
    expect(within(c).queryByRole('button', { name: 'Save' })).toBeNull()

    await userEvent.click(tick(await row('notebooks')))
    // Ticked means read only, until the person picks more.
    expect(within(await row('notebooks')).getByRole('radio', { name: 'Read only' }).getAttribute('aria-checked')).toBe('true')
    await userEvent.click(within(await row('notebooks')).getByRole('radio', { name: 'Read and change' }))
    await userEvent.click(tick(await row('notes.md')))
    expect(sent(api)).toBeUndefined()
    expect(within(c).getByText('Claude Code restarts to pick this up. It takes a few seconds.')).toBeTruthy()

    await userEvent.click(within(c).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(sent(api)).toEqual({
        folders: [{ path: 'data/sales-2026', mode: 'read' }, { path: 'notebooks', mode: 'write' }, { path: 'notes.md', mode: 'read' }],
      }),
    )
    await waitFor(() => expect(within(c).queryByRole('button', { name: 'Save' })).toBeNull())
    expect(tick(await row('notebooks')).checked).toBe(true)
  })

  it('puts back what is saved when the changes are discarded', async () => {
    const api = page(folders([{ path: 'data/sales-2026', mode: 'read' }]))
    renderApp('/apps/cc')
    await userEvent.click(tick(await row('sales-2026')))
    expect(tick(await row('sales-2026')).checked).toBe(false)
    await userEvent.click(within(await card()).getByRole('button', { name: 'Discard changes' }))
    expect(tick(await row('sales-2026')).checked).toBe(true)
    expect(within(await card()).queryByRole('button', { name: 'Save' })).toBeNull()
    expect(sent(api)).toBeUndefined()
  })

  it('lights what is inside a ticked folder, and says why such a row cannot be unticked by itself', async () => {
    const api = page(folders([{ path: 'data/sales-2026', mode: 'read' }]))
    renderApp('/apps/cc')
    await open('sales-2026')
    const q1 = await row('q1.csv')
    expect(q1.textContent).toContain('Included with ~/data/sales-2026')
    expect(q1.textContent).toContain('Read only')
    expect(tick(q1).checked).toBe(true)

    await userEvent.click(tick(q1))
    expect(within(await row('q1.csv')).getByRole('status').textContent).toBe(
      'This is inside ~/data/sales-2026, which is ticked, so Claude Code has access to it too. To leave it out, untick ~/data/sales-2026 and tick only the folders you want.',
    )
    expect(tick(await row('q1.csv')).checked).toBe(true)

    // The project stays open; one explanation shows at a time.
    await userEvent.click(tick(await row('sales-dashboard')))
    expect(within(await row('sales-dashboard')).getByRole('status').textContent).toBe("This is the app's project. Claude Code always reads and changes it.")
    expect(within(await row('q1.csv')).queryByRole('status')).toBeNull()
    expect(tick(await row('sales-dashboard')).checked).toBe(true)
    expect(within(await card()).queryByRole('button', { name: 'Save' })).toBeNull()

    // Ticking the folder around it covers what was ticked to read inside.
    await userEvent.click(tick(await row('data')))
    expect((await row('sales-2026')).textContent).toContain('Included with ~/data')
    await userEvent.click(within(await card()).getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(sent(api)).toEqual({ folders: [{ path: 'data', mode: 'read' }] }))
  })

  it('keeps what is hidden at the top of the home behind a switch, and holds each as the API says', async () => {
    page(folders([]))
    renderApp('/apps/cc')
    await row('notebooks')
    expect(within(await tree()).queryByRole('treeitem', { name: '.ssh' })).toBeNull()
    expect(screen.getByRole('switch', { name: 'Show hidden files and folders' }).getAttribute('aria-checked')).toBe('false')
    await showHidden()

    // Risky: closed until allowed, by its own words or by the words for every other hidden name.
    const ssh = await row('.ssh')
    expect(ssh.textContent).toContain('Holds your SSH keys. Asks you first')
    expect(ssh.textContent).toContain('Risky')
    expect(tick(ssh).checked).toBe(false)
    expect((await row('.npmrc')).textContent).toContain("Holds a tool's settings or sign-ins. Asks you first")

    // Open: every agent has it, to the mode the API names, and it is not the person's to change.
    const own = await row('.claude')
    expect(own.textContent).toContain("Claude Code's own settings. Claude Code always has access")
    expect(own.textContent).toContain('Read and change')
    expect(tick(own).checked).toBe(true)
    expect(tick(own).getAttribute('aria-disabled')).toBe('true')
    expect((await row('.gitconfig')).textContent).toMatch(/Git's settings\. Claude Code always has access.*Read only/)

    // Closed: no tick box, nothing to open.
    const closed = await row('.javo')
    expect(closed.textContent).toContain("Holds the platform's own files. They belong to the platform, not to an agent.")
    expect(within(closed).queryByRole('checkbox')).toBeNull()
    expect(within(closed).queryByRole('button')).toBeNull()

    // Around a closed place: not ticked as a whole, but what is in it follows its own rule.
    const around = await row('.config')
    expect(around.textContent).toContain('To open something else in ~/.config, tick it there, not the whole folder.')
    expect(within(around).queryByRole('checkbox')).toBeNull()
    await open('.config')
    expect((await row('gh')).textContent).toContain('Holds your GitHub sign-in. Asks you first')
    expect((await row('ghostty')).textContent).toContain("Holds a tool's settings or sign-ins. Asks you first")
    const signIn = await row('javo')
    expect(signIn.textContent).toContain('Holds your sign-in to this platform. An agent that has it can lift its own lock.')
    expect(within(signIn).queryByRole('checkbox')).toBeNull()
  })

  it('asks before a risky place is opened, and sends it as allowed', async () => {
    const api = page(folders([]))
    renderApp('/apps/cc')
    await showHidden()
    await userEvent.click(tick(await row('.ssh')))
    const ask = within(await row('.ssh')).getByRole('group', { name: 'Let Claude Code read your SSH keys in ~/.ssh?' })
    expect(within(ask).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Claude Code, and every command it runs, will be able to read them.',
      'Anyone who has these keys can sign in to your servers and push code as you.',
      'Claude Code can use the internet. If a web page, file or package tricks it, these files can be sent anywhere.',
    ])
    expect(tick(await row('.ssh')).checked).toBe(false)

    await userEvent.click(within(ask).getByRole('button', { name: 'Keep no access' }))
    expect(within(await row('.ssh')).queryByRole('group')).toBeNull()
    expect(tick(await row('.ssh')).checked).toBe(false)
    expect(within(await card()).queryByRole('button', { name: 'Save' })).toBeNull()

    await userEvent.click(tick(await row('.ssh')))
    await userEvent.click(within(await row('.ssh')).getByRole('button', { name: 'Let Claude Code read your SSH keys' }))
    const allowed = await row('.ssh')
    expect(allowed.textContent).toContain('Holds your SSH keys. You gave Claude Code access')
    expect(allowed.textContent).not.toContain('Risky')
    expect(tick(allowed).checked).toBe(true)
    expect(within(allowed).getByRole('radio', { name: 'Read only' }).getAttribute('aria-checked')).toBe('true')

    // A hidden name further down is a project's own: no warning.
    await userEvent.click(tick(await row('.github')))
    expect(within(await row('.github')).queryByRole('group')).toBeNull()

    await userEvent.click(within(await card()).getByRole('button', { name: 'Save' }))
    await waitFor(() =>
      expect(sent(api)).toEqual({ folders: [{ path: '.ssh', mode: 'read', risky: true }, { path: 'projects/.github', mode: 'read' }] }),
    )
    // What is allowed stays in view when hidden names are not shown.
    await waitFor(async () => expect(within(await card()).queryByRole('button', { name: 'Save' })).toBeNull())
    await showHidden()
    expect(await row('.ssh')).toBeTruthy()
    expect(within(await tree()).queryByRole('treeitem', { name: '.npmrc' })).toBeNull()
  })

  it('says what the agent can and cannot do in sentences with the real paths, and keeps them up with every tick', async () => {
    page(folders([{ path: 'data/sales-2026', mode: 'read' }]))
    renderApp('/apps/cc')
    await row('notebooks')
    expect(lines('can')).toEqual([
      'Read, edit, add and delete files in ~/projects/sales-dashboard',
      'Read files in ~/data/sales-2026. It cannot change or delete them',
      'Use the internet. This choice covers files only.',
    ])
    expect(lines('cannot')).toEqual([
      'Read or change anything in ~/Documents, ~/notebooks',
      'Read or change anything in ~/data or ~/projects except what is ticked there',
      'Change or delete any file outside the folders it may change',
      "Read ~/.ssh (your SSH keys), ~/.npmrc (a tool's settings or sign-ins), ~/.config/gh (your GitHub sign-in). You have not given it access",
    ])
    expect((await card()).textContent).toContain('Read means look at what is in a file. Change means edit it, add new files, or delete it.')

    await userEvent.click(tick(await row('notebooks')))
    await userEvent.click(within(await row('notebooks')).getByRole('radio', { name: 'Read and change' }))
    await showHidden()
    await userEvent.click(tick(await row('.ssh')))
    await userEvent.click(within(await row('.ssh')).getByRole('button', { name: 'Let Claude Code read your SSH keys' }))
    expect(lines('can')).toEqual([
      'Read, edit, add and delete files in ~/projects/sales-dashboard',
      'Read files in ~/data/sales-2026. It cannot change or delete them',
      'Read, edit, add and delete files in ~/notebooks',
      'Risky: read your SSH keys in ~/.ssh. Anyone who has these keys can sign in to your servers and push code as you.',
      'Use the internet. This choice covers files only.',
    ])
    expect(lines('cannot')).toEqual([
      'Read or change anything in ~/Documents',
      'Read or change anything in ~/data or ~/projects except what is ticked there',
      'Change or delete any file outside the folders it may change',
      "Read ~/.npmrc (a tool's settings or sign-ins), ~/.config/gh (your GitHub sign-in). You have not given it access",
    ])
  })

  it('shows a refusal in the server\'s words and keeps the changes to try again', async () => {
    const refusals: Reply[] = [
      { status: 422, body: { title: 'Unprocessable Entity', errors: [{ location: 'body.folders', message: 'Not added. "notes.md": it is a link.' }] } },
      { status: 403, body: { title: 'Forbidden', detail: 'Only the owner, signed in in the browser, can change what an agent can open.' } },
    ]
    page(folders([]), { 'PUT /api/workspaces/cc/folders': refusals })
    renderApp('/apps/cc')
    await userEvent.click(tick(await row('notes.md')))
    const save = () => within(screen.getByRole('heading', { name: /access\?$/ }).closest('.card') as HTMLElement).getByRole('button', { name: 'Save' })
    await userEvent.click(save())
    expect((await within(await card()).findByRole('alert')).textContent).toBe('Not added. "notes.md": it is a link.')
    expect(tick(await row('notes.md')).checked).toBe(true)
    await userEvent.click(save())
    await waitFor(async () =>
      expect(within(await card()).getByRole('alert').textContent).toBe('Only the owner, signed in in the browser, can change what an agent can open.'),
    )
    // A new choice is a new try: the last refusal goes.
    await userEvent.click(tick(await row('notes.md')))
    expect(within(await card()).queryByRole('alert')).toBeNull()
  })

  it('is worked with the keyboard: arrows move and expand, Space ticks', async () => {
    page(folders([]))
    renderApp('/apps/cc')
    const first = await row('Documents')
    first.focus()
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(await row('data'))
    await userEvent.keyboard('{ArrowRight}')
    expect((await row('data')).getAttribute('aria-expanded')).toBe('true')
    await row('customers')
    await userEvent.keyboard('{ArrowRight}{ArrowDown}')
    expect(document.activeElement).toBe(await row('sales-2026'))
    await userEvent.keyboard(' ')
    expect(tick(await row('sales-2026')).checked).toBe(true)
    await userEvent.keyboard('{ArrowLeft}')
    expect(document.activeElement).toBe(await row('data'))
    await userEvent.keyboard('{ArrowLeft}')
    expect((await row('data')).getAttribute('aria-expanded')).toBe('false')
    // One row is the tab stop: the one last on.
    expect((await row('data')).tabIndex).toBe(0)
    expect((await row('Documents')).tabIndex).toBe(-1)
  })

  it('adds no more than 16, and says so', async () => {
    const full = Array.from({ length: 16 }, (_, i): FolderGrant => ({ path: `archive/${i}`, mode: 'read' }))
    page(folders(full))
    renderApp('/apps/cc')
    await userEvent.click(tick(await row('notebooks')))
    expect(within(await row('notebooks')).getByRole('status').textContent).toBe(
      'You have ticked 16 folders and files, the most one agent can have. Untick one to tick another.',
    )
    expect(tick(await row('notebooks')).checked).toBe(false)
    expect(within(await card()).queryByRole('button', { name: 'Save' })).toBeNull()
  })

  it('shows the project of an app from a repository as the API gives it, above the home', async () => {
    page(folders([], '/workspace/site'))
    renderApp('/apps/cc')
    await row('projects')
    const project = within(await card()).getByText('/workspace/site', { selector: '.mono' }).closest('.tree-row') as HTMLElement
    expect(project.textContent).toContain("This app's project. Claude Code can always read and change it")
    expect(project.textContent).toContain('Read and change')
    expect(lines('can')[0]).toBe('Read, edit, add and delete files in /workspace/site')
    // Nothing in the home leads to it.
    expect((await row('projects')).getAttribute('aria-expanded')).toBe('false')
  })
})
