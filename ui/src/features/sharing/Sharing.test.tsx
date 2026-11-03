import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp, type Call } from '../../test/api'
import { allReady, bob, me, MESSAGES, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

const app = ws('sales-app', 'Sales app', 'Running', allReady())
const lab = ws('lab', 'Research lab', 'Running', allReady(), { kind: 'jupyterlab', template: 'jupyterlab' })

const view = (over: object = {}) => ({
  access: 'Restricted',
  maxAccess: 'Public',
  users: [],
  groups: [],
  link: 'https://apps.example.com/u/alice/sales-app/',
  levels: [
    { value: 'Restricted', label: 'Only people added' },
    { value: 'SignedIn', label: 'Anyone signed in' },
    { value: 'Public', label: 'Anyone with the link' },
  ],
  ...over,
})

function open(sharing: object | object[], extra: Record<string, object> = {}, workspace = app) {
  const api = mockApi({
    'GET /api/me': { body: me },
    [`GET /api/workspaces/${workspace.name}`]: { body: workspace },
    [`GET /api/workspaces/${workspace.name}/sharing`]: (Array.isArray(sharing) ? sharing : [sharing]).map((b) => ({ body: b }))[0],
    ...extra,
  })
  renderApp(`/apps/${workspace.name}`)
  return api
}

async function openSharing() {
  await userEvent.click(await screen.findByRole('tab', { name: 'Sharing' }))
}

describe('Sharing tab', () => {
  it('is hidden when the viewer cannot share the app', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces/sales-app': { body: { ...app, canShare: false } } })
    renderApp('/apps/sales-app')
    await screen.findByRole('heading', { level: 1 })
    expect(screen.queryByRole('tab', { name: 'Sharing' })).toBeNull()
  })

  it('adds a person picked from the directory with a PUT', async () => {
    const api = open(view(), {
      'GET /api/directory': { body: { people: [{ id: bob.id, displayName: 'Bob', email: bob.email }], groups: [] } },
      'PUT /api/workspaces/sales-app/sharing': { body: view({ users: [bob] }) },
    })
    await openSharing()
    await userEvent.type(await screen.findByRole('combobox', { name: 'Add people or groups' }), 'bo')
    await screen.findByRole('option', { name: /Bob/ })
    await userEvent.keyboard('{ArrowDown}{Enter}')
    await waitFor(() => expect(api.calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ access: 'Restricted', users: [bob.id], groups: [] })
    expect(await screen.findByText('bob@example.com')).toBeTruthy()
  })

  it('adds one of the caller\'s groups', async () => {
    const api = open(view(), {
      'GET /api/directory': { body: { people: [], groups: [{ value: '/org/data-science' }] } },
      'PUT /api/workspaces/sales-app/sharing': { body: view({ groups: [{ value: '/org/data-science', label: 'data-science' }] }) },
    })
    await openSharing()
    await userEvent.type(await screen.findByRole('combobox', { name: 'Add people or groups' }), 'da')
    await userEvent.click(await screen.findByRole('option', { name: /data-science/ }))
    await waitFor(() => expect(api.calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ access: 'Restricted', users: [], groups: ['/org/data-science'] })
  })

  it('removes a person with the remove button', async () => {
    const api = open(view({ users: [bob] }), {
      'PUT /api/workspaces/sales-app/sharing': { body: view() },
    })
    await openSharing()
    await userEvent.click(await screen.findByRole('button', { name: 'Remove Bob' }))
    await waitFor(() => expect(api.calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ access: 'Private', users: [], groups: [] })
  })

  it('marks a person whose account is no longer active and still lets you remove them', async () => {
    open(view({ users: [{ ...bob, active: false, note: 'Account no longer active' }] }))
    await openSharing()
    const row = (await screen.findByText('Bob')).closest('li') as HTMLElement
    expect(within(row).getByText('Account no longer active')).toBeTruthy()
    expect(within(row).getByRole('button', { name: 'Remove Bob' })).toBeTruthy()
  })

  it('shows the API message under General access when the change is refused and keeps the selection', async () => {
    open(view({ access: 'Restricted' }), {
      'PUT /api/workspaces/sales-app/sharing': {
        status: 422,
        body: {
          title: MESSAGES.InvalidRequest,
          status: 422,
          errors: [{ location: 'body.access', message: MESSAGES.ShareTooOpenSignedIn }],
        },
      },
    })
    await openSharing()
    await userEvent.click(await screen.findByRole('radio', { name: 'Anyone signed in' }))
    const group = screen.getByRole('group', { name: 'General access' })
    expect((await within(group).findByRole('alert')).textContent).toBe(MESSAGES.ShareTooOpenSignedIn)
    expect(screen.queryByText(MESSAGES.InvalidRequest)).toBeNull()
    expect(screen.getByRole('radio', { name: 'Only people added' }).getAttribute('aria-checked')).toBe('true')
  })

  it('shows the API message for a person the server does not know, not the generic form sentence', async () => {
    open(view({ access: 'Restricted', users: [bob] }), {
      'PUT /api/workspaces/sales-app/sharing': {
        status: 422,
        body: {
          title: MESSAGES.InvalidRequest,
          status: 422,
          errors: [{ location: 'body.users[0]', message: MESSAGES.ShareTargetUnknown(bob.id) }],
        },
      },
    })
    await openSharing()
    await userEvent.click(await screen.findByRole('radio', { name: 'Anyone signed in' }))
    expect((await screen.findByRole('alert')).textContent).toBe(MESSAGES.ShareTargetUnknown(bob.id))
    expect(screen.queryByText(MESSAGES.InvalidRequest)).toBeNull()
  })

  it('does not save when arrow keys move focus onto Anyone with the link', async () => {
    const api = open(view({ access: 'Restricted' }), { 'PUT /api/workspaces/sales-app/sharing': { body: view({ access: 'Public' }) } })
    await openSharing()
    const only = await screen.findByRole('radio', { name: 'Only people added' })
    only.focus()
    await userEvent.keyboard('{ArrowLeft}')
    expect(document.activeElement).toBe(screen.getByRole('radio', { name: 'Anyone with the link' }))
    expect(api.calls.some((c) => c.method === 'PUT')).toBe(false)
    await userEvent.keyboard(' ')
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toMatchObject({ access: 'Public' }))
  })

  it('says plainly that a private-only app cannot be shared, with no controls', async () => {
    open(view({ access: 'Private', maxAccess: 'Private', tooOpen: MESSAGES.ShareTooOpen }), {}, lab)
    await userEvent.click(await screen.findByRole('tab', { name: 'Sharing' }))
    expect(await screen.findByRole('heading', { name: 'Only you can open this app' })).toBeTruthy()
    expect(screen.getByText(/anyone who opens it gets a terminal with your files/)).toBeTruthy()
    expect(screen.queryByRole('radio')).toBeNull()
    expect(screen.queryByRole('combobox')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Copy link' })).toBeNull()
  })

  it('disables the levels the template does not allow and says why', async () => {
    open(view({ access: 'Private', maxAccess: 'SignedIn', tooOpen: MESSAGES.ShareTooOpenSignedIn }))
    await userEvent.click(await screen.findByRole('tab', { name: 'Sharing' }))
    const anyone = await screen.findByRole('radio', { name: 'Anyone with the link' })
    expect(anyone.getAttribute('aria-disabled')).toBe('true')
    expect(screen.getByText(MESSAGES.ShareTooOpenSignedIn)).toBeTruthy()
  })

  it('shows the hint and copies the link without showing it as text', async () => {
    open(view())
    await openSharing()
    expect(await screen.findByText('People you add can use the app. They never get your files, tokens, or sign-in.')).toBeTruthy()
    expect(screen.getByText('Groups come from your sign-in provider. Changes apply within a minute.')).toBeTruthy()
    expect(screen.getByPlaceholderText('Name, email, or group')).toBeTruthy()
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    await userEvent.click(screen.getByRole('button', { name: 'Copy link' }))
    expect(writeText).toHaveBeenCalledWith('https://apps.example.com/u/alice/sales-app/')
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeTruthy()
    expect(screen.queryByRole('heading', { name: 'Link' })).toBeNull()
    expect(document.body.textContent).not.toContain('https://apps.example.com/u/alice/sales-app/')
  })

  describe('while a change is being saved', () => {
    const carol = { id: 'carol-cccccccc', displayName: 'Carol', email: 'carol@example.com', active: true }
    const directory = { body: { people: [{ id: bob.id, displayName: 'Bob', email: bob.email }, { id: carol.id, displayName: 'Carol', email: carol.email }], groups: [] } }

    it('locks the picker, the remove buttons and General access, then adds the second person on top of the first', async () => {
      let release!: () => void
      const hold = new Promise<void>((r) => (release = r))
      let n = 0
      const api = open(view(), {
        'GET /api/directory': directory,
        'PUT /api/workspaces/sales-app/sharing': (call: Call) => {          n++
          const body = call.body as { users: string[] }
          const users = body.users.map((id) => (id === bob.id ? bob : carol))
          return n === 1 ? { body: view({ users }), hold } : { body: view({ users }) }
        },
      })
      await openSharing()
      const picker = await screen.findByRole('combobox', { name: 'Add people or groups' })
      await userEvent.type(picker, 'bo')
      await userEvent.click(await screen.findByRole('option', { name: /Bob/ }))

      await waitFor(() => expect((screen.getByRole('combobox', { name: 'Add people or groups' }) as HTMLInputElement).disabled).toBe(true))
      expect(screen.getByRole('radio', { name: 'Anyone signed in' }).getAttribute('aria-disabled')).toBe('true')
      release()

      const again = screen.getByRole('combobox', { name: 'Add people or groups' }) as HTMLInputElement
      await waitFor(() => expect(again.disabled).toBe(false))
      expect(await screen.findByRole('button', { name: 'Remove Bob' })).toHaveProperty('disabled', false)
      await userEvent.type(again, 'ca')
      await userEvent.click(await screen.findByRole('option', { name: /Carol/ }))
      await waitFor(() => expect(api.calls.filter((c) => c.method === 'PUT')).toHaveLength(2))
      expect(api.calls.filter((c) => c.method === 'PUT')[1].body).toEqual({ access: 'Restricted', users: [bob.id, carol.id], groups: [] })
    })

    it('locks the remove buttons', async () => {
      let release!: () => void
      const hold = new Promise<void>((r) => (release = r))
      open(view({ users: [bob, carol] }), { 'PUT /api/workspaces/sales-app/sharing': { body: view({ users: [carol] }), hold } })
      await openSharing()
      await userEvent.click(await screen.findByRole('button', { name: 'Remove Bob' }))
      await waitFor(() => expect(screen.getByRole('button', { name: 'Remove Carol' })).toHaveProperty('disabled', true))
      release()
      await waitFor(() => expect(screen.getByRole('button', { name: 'Remove Carol' })).toHaveProperty('disabled', false))
    })
  })
})
