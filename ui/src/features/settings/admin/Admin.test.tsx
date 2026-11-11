import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { AdminApp, BackupRunSummary, BackupsOutputBody, TransferRow } from '../../../api'
import { mockApi, renderApp } from '../../../test/api'
import { adminMe, allReady, allStopped, me, ws } from '../../../test/fixtures'
import { choose, optionsOf } from '../../../test/select'

const plots: AdminApp = {
  id: 'a-1',
  owner: { name: 'alice-aaaaaaaa', displayName: 'Alice' },
  app: ws('plots', 'Plots', 'Running', allReady()),
  createdAt: '2026-10-01T09:00:00Z',
  lastActiveAt: '2026-11-01T09:00:00Z',
  canOpen: true,
}
const notes: AdminApp = { ...plots, id: 'a-2', app: ws('notes', 'Notes', 'Stopped', allStopped()) }

const backups: BackupsOutputBody = {
  database: {
    engine: 'sqlite',
    schedule: '0 2 * * *',
    keep: 7,
    canBackUpNow: true,
    status: { reason: 'DatabaseBackedUp', title: 'Database backed up', what: 'The database is copied on schedule.', todo: '', problem: false },
    items: [{ name: 'javo-20261101T020000Z.db', takenAt: '2026-11-01T02:00:00Z', size: 2048 }],
  },
  homeFolders: { reason: 'NoBackupTool', title: 'Home folders not backed up', what: 'Files in home folders are in no backup.', todo: 'Turn on backups to a bucket.', problem: true },
}

const ok = (reason: string, title: string) => ({ reason, title, what: '', todo: '', problem: false })
const run = (n: number, outcome: string, extra: Partial<BackupRunSummary> = {}): BackupRunSummary => ({
  run: `backup-r${n}`,
  runId: `run-${n}`,
  trigger: 'schedule',
  startedAt: `2026-11-0${n}T01:00:00Z`,
  finishedAt: `2026-11-0${n}T01:05:00Z`,
  outcome,
  status: outcome === 'Succeeded' ? ok('FullBackupDone', 'Everything is backed up') : { ...ok('FullBackupProblems', 'The newest backup missed something'), problem: true },
  homes: 0,
  homesFailed: 0,
  volumes: 0,
  volumesFailed: 0,
  folders: 0,
  foldersFailed: 0,
  ...extra,
})
const failingHome = {
  id: 'h-2',
  owner: 'local-alice-6f5951a4',
  person: 'Home volume with no account',
  account: false,
  claim: 'home-local-alice-6f5951a4',
  namespace: 'javo-apps',
  status: { reason: 'BackupDenied', title: 'Backup bucket refused the key', what: 'The bucket refused the access key.', todo: 'Check the key.', problem: true },
  lastAt: '2026-11-01T01:02:00Z',
  evidence: ['Access Denied'],
}
const bucketBackups: BackupsOutputBody = {
  database: backups.database,
  bucket: {
    target: { endpoint: 's3.example.com', bucket: 'backups', prefix: 'javo' },
    jobs: {
      backup: { id: 'j-1', name: 'backup', owner: 'admin-00000000', schedule: '0 1 * * *', timeZone: 'Europe/London', paused: false },
      restore: { id: 'j-2', name: 'restore', owner: 'admin-00000000', paused: false },
    },
    status: { reason: 'FullBackupProblems', title: 'The newest backup missed something', what: 'Some parts were not copied.', todo: 'Read why.', problem: true },
    lastRun: run(2, 'Failed', { database: 'javo-20261102T010000Z.db', homes: 2, volumes: 1, volumesFailed: 1, folders: 1 }),
    runs: [run(2, 'Failed'), run(1, 'Succeeded')],
    keepDaily: 7,
    keepWeekly: 4,
    keepMonthly: 6,
    homes: [
      failingHome,
      { id: 'h-1', owner: 'alice-aaaaaaaa', person: 'Alice Example (alice, local)', account: true, claim: 'home-alice-aaaaaaaa', namespace: 'javo-apps', status: ok('HomeBackedUp', 'Backed up'), lastAt: '2026-11-02T01:01:00Z', evidence: [] },
      { id: 'h-3', owner: 'alice-cccccccc', person: 'alice (SSO)', account: true, claim: 'home-alice-cccccccc', namespace: 'javo-apps', status: ok('HomeBackedUp', 'Backed up'), lastAt: '2026-11-02T01:01:00Z', evidence: [] },
    ],
    folders: [{ id: 'f-1', owner: 'data', person: '/data-science', account: true, claim: 'folder-data', namespace: 'javo-apps', status: ok('HomeBackedUp', 'Backed up'), lastAt: '2026-11-02T01:03:00Z', evidence: [] }],
  },
}
const notSetUp: BackupsOutputBody = {
  ...backups,
  bucket: {
    ...bucketBackups.bucket!,
    jobs: {},
    status: { reason: 'BackupsNotSetUp', title: 'Backups not set up', what: 'There is no backup job yet.', todo: 'Press Set up backups.', problem: true },
    lastRun: undefined,
    runs: [],
  },
}

const row = (name: string, outcome: TransferRow['outcome'], message: string): TransferRow => ({
  name,
  displayName: name,
  template: 'streamlit',
  owner: 'alice-aaaaaaaa',
  outcome,
  message,
  wasRunning: true,
})

function admin(routes: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    'GET /api/me': { body: adminMe },
    'GET /api/info': { body: { apiDocs: false } },
    'GET /api/tokens': { body: [] },
    'GET /api/admin/apps': { body: { items: [plots, notes] } },
    'GET /api/admin/backups': { body: backups },
    'GET /api/admin/restores': { body: { items: [] } },
    ...routes,
  })
}

describe('Settings for platform admins', () => {
  it('shows the admin sections only to platform admins', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/info': { body: { apiDocs: false } }, 'GET /api/tokens': { body: [] } })
    renderApp('/settings')
    await screen.findByRole('link', { name: 'API tokens', current: 'page' })
    expect(screen.queryByRole('link', { name: 'Backups' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Storage' })).toBeNull()
  })

  it.each([
    ['/settings', 'API tokens', /New token/],
    ['/settings/secrets', 'Secrets', /New secret/],
    ['/settings/ai', 'AI', /AI is off/],
    ['/settings/agents', 'Agents', /act without asking/],
  ])('names %s once, in its tab, not again as a heading', async (path, name, loaded) => {
    admin({
      'GET /api/secrets': { body: [] },
      'GET /api/admin/ai': { body: { enabled: false } },
      'GET /api/admin/agents': { body: { allowTrusted: true, configAllows: true } },
    })
    renderApp(path)
    await screen.findByRole('link', { name, current: 'page' })
    await screen.findAllByText(loaded)
    expect(screen.queryByRole('heading', { level: 2, name })).toBeNull()
  })

  it('says so when a member opens an admin page', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/admin/apps': { status: 403, body: { title: 'Only platform admins can do this.' } },
    })
    renderApp('/settings/apps')
    expect(await screen.findByText('Only platform admins can do this.')).toBeTruthy()
  })

  it('lists every person’s apps and stops one after a confirmation naming the app and owner', async () => {
    const api = admin({
      'POST /api/workspaces/plots/stop': { body: { ...plots.app, phase: 'Stopping' } },
    })
    renderApp('/settings')
    await userEvent.click(await screen.findByRole('link', { name: 'All apps' }))
    const table = await screen.findByRole('table', { name: 'All apps' })
    const r = within(table).getByRole('row', { name: /Plots/ })
    expect(within(r).getByText('Alice')).toBeTruthy()
    expect(within(within(table).getByRole('row', { name: /Notes/ })).queryByRole('button', { name: /Stop/ })).toBeNull()
    await userEvent.click(within(r).getByRole('button', { name: 'Stop Plots' }))
    const dialog = screen.getByRole('alertdialog')
    expect(dialog.textContent).toContain('Stop Plots of Alice?')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Stop' }))
    expect(api.calls.find((c) => c.method === 'POST')?.path).toBe('/api/workspaces/plots/stop?owner=alice-aaaaaaaa')
  })

  it('starts a stopped app of any person, as that app\u2019s owner', async () => {
    const api = admin({ 'POST /api/workspaces/notes/start': { body: { ...notes.app, phase: 'Starting' } } })
    renderApp('/settings/apps')
    const table = await screen.findByRole('table', { name: 'All apps' })
    expect(within(within(table).getByRole('row', { name: /Plots/ })).queryByRole('button', { name: /^Start/ })).toBeNull()
    await userEvent.click(within(within(table).getByRole('row', { name: /Notes/ })).getByRole('button', { name: 'Start Notes' }))
    expect(api.calls.find((c) => c.method === 'POST')?.path).toBe('/api/workspaces/notes/start?owner=alice-aaaaaaaa')
  })

  it('links each running app the admin may open, in a new tab, and says why the others do not open', async () => {
    const maps: AdminApp = { ...plots, id: 'a-3', canOpen: false, owner: { name: 'carol-cccccccc', displayName: 'Carol' }, app: ws('maps', 'Maps', 'Running', allReady()) }
    admin({ 'GET /api/admin/apps': { body: { items: [plots, notes, maps] } } })
    renderApp('/settings/apps')
    const table = await screen.findByRole('table', { name: 'All apps' })
    const open = within(within(table).getByRole('row', { name: /Plots/ })).getByRole('link', { name: 'Open Plots' })
    expect(open.getAttribute('href')).toBe('/u/alice/plots/')
    expect(open.getAttribute('target')).toBe('_blank')
    expect(within(within(table).getByRole('row', { name: /Notes/ })).queryByRole('link', { name: /Open/ })).toBeNull()
    expect(within(within(table).getByRole('row', { name: /Maps/ })).queryByRole('link', { name: /Open/ })).toBeNull()
    expect(screen.getByText(/only when it is shared with you/)).toBeTruthy()
  })

  it('says how long each running app has run, from when it last became ready', async () => {
    const minutes = (n: number) => new Date(Date.now() - n * 60000).toISOString()
    const running = (id: string, name: string, readyAt: string): AdminApp => ({
      ...plots,
      id,
      app: ws(name.toLowerCase(), name, 'Running', allReady().map((s) => (s.type === 'Ready' ? { ...s, since: readyAt } : s))),
    })
    admin({ 'GET /api/admin/apps': { body: { items: [running('a-1', 'Plots', minutes(134)), running('a-3', 'Maps', minutes(3 * 1440 + 5)), notes] } } })
    renderApp('/settings/apps')
    const table = await screen.findByRole('table', { name: 'All apps' })
    expect(within(within(table).getByRole('row', { name: /Plots/ })).getByText('Running for 2 h 14 min')).toBeTruthy()
    expect(within(within(table).getByRole('row', { name: /Maps/ })).getByText('Running for 3 days')).toBeTruthy()
    expect(within(within(table).getByRole('row', { name: /Notes/ })).queryByText(/Running for/)).toBeNull()
  })

  it('names owners by display name, with the username as a tooltip only when two people share a name', async () => {
    const owned = (id: string, name: string, owner: AdminApp['owner']): AdminApp => ({ ...plots, id, owner, app: ws(name.toLowerCase(), name, 'Stopped', allStopped()) })
    admin({
      'GET /api/admin/apps': {
        body: {
          items: [
            owned('a-1', 'Plots', { name: 'alex-aaaaaaaa', displayName: 'Alex', username: 'alex' }),
            owned('a-2', 'Maps', { name: 'alex-bbbbbbbb', displayName: 'Alex', username: 'alex.b' }),
            owned('a-3', 'Notes', { name: 'bob-cccccccc', displayName: 'Bob', username: 'bob' }),
          ],
        },
      },
    })
    renderApp('/settings/apps')
    const table = await screen.findByRole('table', { name: 'All apps' })
    expect(within(within(table).getByRole('row', { name: /Maps/ })).getByText('Alex').getAttribute('title')).toBe('alex.b')
    expect(within(within(table).getByRole('row', { name: /Plots/ })).getByText('Alex').getAttribute('title')).toBe('alex')
    expect(within(within(table).getByRole('row', { name: /Notes/ })).getByText('Bob').getAttribute('title')).toBeNull()
  })

  it('shows database and home folder backups, with problems in the error style', async () => {
    admin()
    renderApp('/settings/backups')
    expect(await screen.findByText('Database backed up')).toBeTruthy()
    const homes = screen.getByText('Home folders not backed up').closest('.banner')!
    expect(homes.className).toContain('err')
    expect(screen.getByText('javo-20261101T020000Z.db')).toBeTruthy()
  })

  it('sums up the newest full backup in one status with what it included', async () => {
    admin({ 'GET /api/admin/backups': { body: bucketBackups } })
    renderApp('/settings/backups')
    const status = await screen.findByText('The newest backup missed something')
    expect(status.closest('.banner')!.className).toContain('err')
    expect(screen.getAllByText('The newest backup missed something')).toHaveLength(1)
    expect(screen.getByText(/the database, 2 home folders of people, 1 home volume with no account and 1 shared folder; 1 failed/)).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Open the run and its log' }).getAttribute('href')).toBe('/jobs/backup')
    // The server leaves out the home folders' status once backups go to the bucket.
    expect(screen.queryByText('Home folders not backed up')).toBeNull()
  })

  it('backs everything up now with one button', async () => {
    const api = admin({
      'GET /api/admin/backups': { body: bucketBackups },
      'POST /api/admin/backups/now': { status: 201, body: { job: 'backup', owner: 'admin-00000000', runId: 'run-3' } },
    })
    renderApp('/settings/backups')
    await userEvent.click(await screen.findByRole('button', { name: 'Back up now' }))
    expect(api.count('POST', '/api/admin/backups/now')).toBe(1)
    expect(api.count('POST', '/api/admin/backups')).toBe(0)
    expect(await screen.findByRole('link', { name: 'Follow the backup' })).toBeTruthy()
  })

  it('sets up backups as two ordinary jobs', async () => {
    const api = admin({
      'GET /api/admin/backups': { body: notSetUp },
      'POST /api/jobs': { status: 201, body: {} },
    })
    renderApp('/settings/backups')
    await userEvent.click(await screen.findByRole('button', { name: 'Set up backups' }))
    const posts = api.calls.filter((c) => c.method === 'POST' && c.path === '/api/jobs').map((c) => c.body as Record<string, unknown>)
    expect(posts).toHaveLength(2)
    expect(posts[0]).toMatchObject({ displayName: 'Backup', template: 'backup', engine: 'command', command: '/javo backup run', schedule: { cron: '0 1 * * *' } })
    expect(posts[1]).toMatchObject({ displayName: 'Restore', template: 'backup', engine: 'command', command: '/javo backup restore-run' })
    expect(posts[1].schedule).toBeUndefined()
    expect((posts[1].parameters as { name: string }[]).map((p) => p.name)).toEqual(['from', 'person', 'mode'])
  })

  it('restores one person beside their files from a chosen date', async () => {
    const api = admin({
      'GET /api/admin/backups': { body: bucketBackups },
      'POST /api/admin/backups/restore': { status: 201, body: { job: 'restore', owner: 'admin-00000000', runId: 'run-9' } },
    })
    renderApp('/settings/backups')
    // The older backup, the one without problems.
    await choose(await screen.findByRole('combobox', { name: 'Backup to restore' }), /^(?!.*with problems)/)
    // Two people called alice tell apart by display name, username and how they sign in.
    const whose = screen.getByRole('combobox', { name: 'Whose files' })
    expect(await optionsOf(whose)).toEqual(expect.arrayContaining(['Alice Example (alice, local)', 'alice (SSO)']))
    await choose(whose, 'Alice Example (alice, local)')
    await userEvent.click(screen.getByRole('radio', { name: /Put beside current files/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Restore' }))
    const post = api.calls.find((c) => c.method === 'POST' && c.path === '/api/admin/backups/restore')
    expect(post?.body).toEqual({ from: 'backup-r1', person: 'alice-aaaaaaaa', mode: 'beside' })
    expect(await screen.findByRole('link', { name: 'Follow the restore' })).toBeTruthy()
  })

  it('keeps each home and folder behind Show, problems first', async () => {
    admin({ 'GET /api/admin/backups': { body: bucketBackups } })
    renderApp('/settings/backups')
    await screen.findByText('The newest backup missed something')
    expect(screen.queryByRole('list', { name: 'Each home folder and shared folder' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /Show each home folder and shared folder/ }))
    const items = within(screen.getByRole('list', { name: 'Each home folder and shared folder' })).getAllByRole('listitem')
    // Who it is first, the id small; a volume with no account says so, with its claim.
    expect(items.map((i) => i.querySelector('.volume-name')?.textContent)).toEqual([
      'Home volume with no account: home-local-alice-6f5951a4',
      'Alice Example (alice, local)',
      'alice (SSO)',
      'Shared folder data (/data-science)',
    ])
    expect(items[1].querySelector('.mono')?.textContent).toBe('alice-aaaaaaaa')
    expect(within(items[0]).getByText('Backup bucket refused the key').className).toContain('errmsg')
  })

  it('backs up now', async () => {
    const api = admin({ 'POST /api/admin/backups': { status: 201, body: backups.database.items![0] } })
    renderApp('/settings/backups')
    await userEvent.click(await screen.findByRole('button', { name: 'Back up now' }))
    expect(api.count('POST', '/api/admin/backups')).toBe(1)
  })

  it('previews a restore with problems first, then restores the apps that come back', async () => {
    const preview = [
      row('gone', 'Create', 'Will come back and wait for you to start the restored apps.'),
      row('kept', 'Exists', 'An app with this name and owner exists now. It is left as it is.'),
      row('old', 'TemplateMissing', 'Its template retired does not exist here. Add the template first.'),
    ]
    const api = admin({
      'POST /api/admin/backups/javo-20261101T020000Z.db/preview': { body: { rows: preview } },
      'POST /api/admin/backups/javo-20261101T020000Z.db/restore': { body: { rows: [{ ...preview[0], outcome: 'Created' }, preview[1], preview[2]] } },
    })
    renderApp('/settings/backups')
    await userEvent.click(await screen.findByRole('button', { name: 'Preview restore from javo-20261101T020000Z.db' }))
    const list = await screen.findByRole('list', { name: 'Restore preview' })
    const items = within(list).getAllByRole('listitem')
    expect(items[0].textContent).toContain('old')
    expect(items[0].textContent).toContain('does not exist here')
    // The apps that come back are behind Show more.
    expect(within(list).queryByText('gone')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Bring back 1 app' }))
    expect(api.count('POST', '/api/admin/backups/javo-20261101T020000Z.db/restore')).toBe(1)
  })

  it('releases restored apps at a rate or leaves them stopped', async () => {
    const api = admin({
      'GET /api/admin/restores': { body: { items: [{ name: 'javo-20261101T020000Z.db', held: 5, scheduled: 0, wasRunning: 3 }] } },
      'POST /api/admin/restores/javo-20261101T020000Z.db/release': { body: { released: 5 } },
    })
    renderApp('/settings/backups')
    expect(await screen.findByText(/5 apps wait, 3 were running/)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Start 10 per minute' }))
    await userEvent.click(screen.getByRole('button', { name: 'Leave stopped' }))
    const bodies = api.calls.filter((c) => c.path.endsWith('/release')).map((c) => c.body)
    expect(bodies).toEqual([{ mode: 'start', perMinute: 10 }, { mode: 'stopped' }])
  })

  it('shows app-hours per person', async () => {
    admin({
      'GET /api/admin/usage': {
        body: {
          since: '2026-10-25T12:00:00Z',
          totals: { hours: 12.5, people: 2, runningNow: 1 },
          items: [{ key: 'alice-aaaaaaaa', displayName: 'Alice', hours: 10, apps: 2, people: 1, runningNow: 1 }],
        },
      },
    })
    renderApp('/settings/usage')
    const table = await screen.findByRole('table', { name: 'Usage' })
    expect(within(table).getByRole('row', { name: /Alice/ }).textContent).toContain('10')
    expect(screen.getByText('12.5')).toBeTruthy()
  })
})
