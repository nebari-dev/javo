import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { StepLogRecord } from '../../api'
import { location, mockApi, renderApp } from '../../test/api'
import { FakeEventSource } from '../../test/eventsource'
import { allowance, allReady, allStopped, containerTemplate, customTemplate, singleNodeSizes, me, NOTICES, NPM_ERRORS, previewApp, REASONS, repoApp, SITE_TYPES, sizes, staticTemplate, stepsAt, storageView, templates, TOO_BIG, withSince, ws } from '../../test/fixtures'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

function detail(...replies: object[]) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces/sales-app': replies.map((body) => ({ body })),
  })
}

/** A step log answer: one JSON record per line. */
const records = (...rs: StepLogRecord[]) => rs.map((r) => JSON.stringify(r) + '\n').join('')

/** Matches a log line by its whole text, which highlighting splits into parts. */
const logText = (text: string) => (_: string, el: Element | null) => !!el?.matches('.log-text') && el.textContent === text

describe('App status page', () => {
  it('shows the five startup steps with the current one marked', async () => {
    detail(ws('sales-app', 'Sales app', 'Starting', stepsAt(2, 'Starting')))
    renderApp('/apps/sales-app')
    const steps = await screen.findByRole('list', { name: 'Startup steps' })
    const items = within(steps).getAllByRole('listitem')
    expect(items).toHaveLength(5)
    expect(items[2].getAttribute('aria-current')).toBe('step')
    expect(items.map((i) => i.dataset.state)).toEqual(['done', 'done', 'now', 'todo', 'todo'])
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Sales app')
  })

  it('renders a failed step as a failure card with the rest behind More', async () => {
    detail(ws('sales-app', 'Sales app', 'Failed', stepsAt(3, 'AppCrashed', ["container app terminated: Error, exit code 1"])))
    renderApp('/apps/sales-app')
    const card = await screen.findByRole('alert')
    expect(within(card).getByText('App crashed on start')).toBeTruthy()
    expect(within(card).getByText(REASONS.AppCrashed.what)).toBeTruthy()
    expect(within(card).getByText(REASONS.AppCrashed.todo)).toBeTruthy()
    expect(within(card).queryByText(REASONS.AppCrashed.who)).toBeNull()
    expect(within(card).queryByText(/exit code 1/)).toBeNull()
    await userEvent.click(within(card).getByRole('button', { name: /Who can help/ }))
    expect(within(card).getByText(REASONS.AppCrashed.who)).toBeTruthy()
    expect(within(card).getByText(REASONS.AppCrashed.blame)).toBeTruthy()
    expect(within(card).getByText(/exit code 1/)).toBeTruthy()
    const steps = screen.getByRole('list', { name: 'Startup steps' })
    expect(within(steps).getAllByRole('listitem')[3].dataset.state).toBe('bad')
  })

  it('shows a failed build with its error lines on the card and its full log in the Build step', async () => {
    const site = ws('site', 'Docs site', 'Failed', stepsAt(3, 'BuildFailed', NPM_ERRORS, SITE_TYPES), { template: 'static-site', kind: 'static', file: '' })
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/site': { body: site },
      'GET /api/workspaces/site/events': { body: { items: [] } },
      'GET /api/workspaces/site/logs': { body: { lines: [], restarts: 0 } },
      'GET /api/workspaces/site/steps/Built/log': {
        body: records(
          { kind: 'start', container: 'build' },
          { kind: 'line', container: 'build', text: '> site@1.0.0 build' },
          { kind: 'line', container: 'build', text: 'npm error code EUSAGE' },
          { kind: 'line', container: 'build', text: 'npm error A complete log of this run can be found in: /tmp/npm/x.log' },
          { kind: 'exit', container: 'build', exitCode: 1, reason: 'Error' },
        ),
      },
    })
    renderApp('/apps/site')
    const card = await screen.findByRole('alert')
    expect(within(card).getByText('Build failed')).toBeTruthy()
    const output = within(card).getByRole('region', { name: 'Build output' })
    for (const line of NPM_ERRORS.slice(1)) expect(within(output).getByText(logText(line))).toBeTruthy()

    // The steps themselves, not the log lines inside them.
    const items = Array.from(screen.getByRole('list', { name: 'Startup steps' }).children) as HTMLElement[]
    expect(items).toHaveLength(6)
    expect(within(items[3]).getByText('Build the site')).toBeTruthy()
    expect(items[3].dataset.state).toBe('bad')
    const log = await within(items[3]).findByRole('region', { name: 'Output' })
    expect(within(log).getByText(logText('npm error A complete log of this run can be found in: /tmp/npm/x.log'))).toBeTruthy()
    expect(within(log).getByText(logText('build exited with code 1 (Error)'))).toBeTruthy()
    expect(api.count('GET', '/api/workspaces/site/steps/Built/log')).toBeGreaterThan(0)
  })

  it('shows a waiting problem as a failure card while the step is still in progress', async () => {
    detail(ws('sales-app', 'Sales app', 'Starting', stepsAt(0, 'NoCapacity')))
    renderApp('/apps/sales-app')
    const card = await screen.findByRole('alert')
    expect(within(card).getByText('No machine has room')).toBeTruthy()
    const steps = screen.getByRole('list', { name: 'Startup steps' })
    expect(within(steps).getAllByRole('listitem')[0].dataset.state).toBe('now')
  })

  it('shows next to Running how long the app has been up, from its Ready time', async () => {
    const ready = new Date(Date.now() - (2 * 3600 + 14 * 60 + 30) * 1000).toISOString()
    detail(ws('sales-app', 'Sales app', 'Running', withSince(allReady(), ready, [0, 0, 0, 0, 0])))
    renderApp('/apps/sales-app')
    const up = await screen.findByText('2 h 14 min')
    expect(up.closest('.dhead')).not.toBeNull()
    expect(up.getAttribute('dateTime')).toBe(ready)
  })

  it('says Started in n s from the API step times, also for an app that was already running', async () => {
    const steps = withSince(allReady(), '2026-10-30T12:00:00Z', [0, 1, 30, 31, 42])
    detail(ws('sales-app', 'Sales app', 'Running', steps))
    renderApp('/apps/sales-app')
    const more = await screen.findByRole('button', { name: 'Started in 42 s: show the startup steps' })
    // A green tick marks the successful start.
    expect(more.querySelector('svg.started-ok')).not.toBeNull()
    expect(screen.queryByRole('list', { name: 'Startup steps' })).toBeNull()
    await userEvent.click(more)
    const items = within(screen.getByRole('list', { name: 'Startup steps' })).getAllByRole('listitem')
    expect(items).toHaveLength(5)
    // Each step shows how long it took after the one before it.
    expect(within(items[2]).getByText('29 s')).toBeTruthy()
    expect(within(items[4]).getByText('11 s')).toBeTruthy()
    expect(screen.getByRole('link', { name: /Open/ }).getAttribute('href')).toBe('/u/alice/sales-app/')
    expect(screen.getByRole('link', { name: /Open/ }).getAttribute('target')).toBe('_blank')
  })

  it('says Started without a time when the API sends no step times', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces/old-app': { body: ws('old-app', 'Old app', 'Running', allReady()) } })
    renderApp('/apps/old-app')
    expect(await screen.findByRole('button', { name: 'Started: show the startup steps' })).toBeTruthy()
  })

  it('opens an app served on a separate apps host, and never shows its address as text', async () => {
    const url = 'https://apps.example.com/u/alice/sales-app/'
    detail(ws('sales-app', 'Sales app', 'Running', allReady(), { url }))
    renderApp('/apps/sales-app')
    await screen.findByRole('heading', { level: 1 })
    expect(screen.getByRole('link', { name: /Open/ }).getAttribute('href')).toBe(url)
    expect(document.body.textContent).not.toContain(url)
    expect(document.body.textContent).not.toContain('/u/alice/sales-app/')
  })

  it('offers no Open link for an address that is not http or an app path', async () => {
    detail(ws('sales-app', 'Sales app', 'Running', allReady(), { url: 'javascript:alert(1)' }))
    renderApp('/apps/sales-app')
    await screen.findByRole('heading', { level: 1 })
    expect(screen.queryByRole('link', { name: /Open/ })).toBeNull()
  })

  it('restarts an app by stopping then starting it', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Failed', stepsAt(3, 'AppCrashed')) },
      'POST /api/workspaces/sales-app/stop': { body: ws('sales-app', 'Sales app', 'Stopped', allStopped()) },
      'POST /api/workspaces/sales-app/start': { body: ws('sales-app', 'Sales app', 'Pending', stepsAt(0, 'Starting')) },
    })
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('button', { name: 'Restart' }))
    await waitFor(() => expect(api.count('POST', '/api/workspaces/sales-app/start')).toBe(1))
    const posts = api.calls.filter((c) => c.method === 'POST').map((c) => c.path)
    expect(posts).toEqual(['/api/workspaces/sales-app/stop', '/api/workspaces/sales-app/start'])
  })

  it('deletes an app only after a confirmation that names it', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': { body: { items: [] } },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Running', allReady()) },
      'DELETE /api/workspaces/sales-app': { status: 204 },
    })
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('button', { name: 'Delete' }))
    const confirm = await screen.findByRole('alertdialog', { name: /Delete Sales app\?/ })
    await userEvent.click(within(confirm).getByRole('button', { name: 'Cancel' }))
    expect(api.count('DELETE', '/api/workspaces/sales-app')).toBe(0)
    expect(screen.queryByRole('alertdialog')).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: 'Delete' }))
    const again = await screen.findByRole('alertdialog', { name: /Delete Sales app\?/ })
    await userEvent.click(within(again).getByRole('button', { name: 'Delete Sales app' }))
    await waitFor(() => expect(location()).toBe('/apps'))
    expect(api.count('DELETE', '/api/workspaces/sales-app')).toBe(1)
  })

  it('asks before stopping, and shows Stopping until the app is stopped', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': [
        { body: ws('sales-app', 'Sales app', 'Running', allReady()) },
        { body: ws('sales-app', 'Sales app', 'Stopped', allStopped()) },
      ],
      'POST /api/workspaces/sales-app/stop': { body: ws('sales-app', 'Sales app', 'Stopping', allReady()) },
    })
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('button', { name: 'Stop' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Stop Sales app?' })
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(api.count('POST', '/api/workspaces/sales-app/stop')).toBe(0)
    expect(screen.queryByRole('alertdialog')).toBeNull()

    await userEvent.click(screen.getByRole('button', { name: 'Stop' }))
    await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop Sales app' }))
    expect(api.count('POST', '/api/workspaces/sales-app/stop')).toBe(1)
    expect(await screen.findByText('Stopping')).toBeTruthy()
    // Polling picks up the stopped app: it can be started again.
    expect(await screen.findByRole('button', { name: 'Start' }, { timeout: 4000 })).toBeTruthy()
  })

  it('calls Stop "Stop retrying" on a failed app and says what it does', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Failed', stepsAt(3, 'AppCrashed')) },
      'POST /api/workspaces/sales-app/stop': { body: ws('sales-app', 'Sales app', 'Stopped', allStopped()) },
    })
    renderApp('/apps/sales-app')
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull()
    await userEvent.click(await screen.findByRole('button', { name: 'Stop retrying' }))
    const dialog = screen.getByRole('alertdialog', { name: 'Stop retrying Sales app?' })
    expect(within(dialog).getByText(/stops restarting it and frees its CPU and memory/)).toBeTruthy()
    await userEvent.click(within(dialog).getByRole('button', { name: 'Stop retrying' }))
    expect(api.count('POST', '/api/workspaces/sales-app/stop')).toBe(1)
  })

  it('sends the user to sign in when an action answers 401', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Running', allReady()) },
      'POST /api/workspaces/sales-app/stop': { status: 401, body: { title: 'Sign in required', status: 401 } },
    })
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('button', { name: 'Stop' }))
    await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Stop Sales app' }))
    await waitFor(() => expect(location()).toBe('/login?next=%2Fapps%2Fsales-app'))
    expect(screen.getByText('Your session ended. Sign in again.')).toBeTruthy()
  })

  it('shows the template display name in the details', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items: templates } },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Running', allReady()) },
    })
    renderApp('/apps/sales-app')
    const details = (await screen.findByText('What you asked for')).closest('.card') as HTMLElement
    await waitFor(() => expect(within(details).getByText('Streamlit')).toBeTruthy())
  })

  it('shows a stopped app with the API text and starts it', async () => {
    const stopped = ws('sales-app', 'Sales app', 'Stopped', allStopped())
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': [{ body: stopped }],
      'POST /api/workspaces/sales-app/start': { body: ws('sales-app', 'Sales app', 'Pending', stepsAt(0, 'Starting')) },
    })
    renderApp('/apps/sales-app')
    expect(await screen.findByText(REASONS.Stopped.what)).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(api.count('POST', '/api/workspaces/sales-app/start')).toBe(1))
    expect(await screen.findByRole('list', { name: 'Startup steps' })).toBeTruthy()
  })

  it('expands a startup step to show its real Kubernetes events', async () => {
    const steps = withSince(allReady(), '2026-10-30T12:00:00Z', [0, 1, 30, 31, 42])
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Running', steps) },
      'GET /api/workspaces/sales-app/events': {
        body: {
          items: [
            { step: 'Scheduled', time: '2026-10-30T12:00:00Z', type: 'Normal', reason: 'Scheduled', message: 'Successfully assigned javo-apps/x to node-1', count: 1 },
            { step: 'ImageReady', time: '2026-10-30T12:00:02Z', type: 'Normal', reason: 'Pulling', message: 'Pulling image "streamlit:1"', count: 1 },
          ],
        },
      },
      'GET /api/workspaces/sales-app/logs': { body: { lines: [], restarts: 0 } },
    })
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('button', { name: /Started in 42 s/ }))
    const items = within(screen.getByRole('list', { name: 'Startup steps' })).getAllByRole('listitem')
    expect(within(items[0]).queryByText(/Successfully assigned/)).toBeNull()
    await userEvent.click(within(items[0]).getByRole('button', { name: /Show events and log lines/ }))
    expect(await within(items[0]).findByText(logText('Successfully assigned javo-apps/x to node-1'))).toBeTruthy()
    expect(within(items[0]).queryByText(/Pulling image/)).toBeNull()
  })

  it('opens the failed step with its events and the app log lines', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Failed', stepsAt(3, 'AppCrashed')) },
      'GET /api/workspaces/sales-app/events': {
        body: {
          items: [
            { step: 'Started', time: '2026-10-30T12:00:05Z', type: 'Warning', reason: 'BackOff', message: 'Back-off restarting failed container app', count: 4 },
          ],
        },
      },
      'GET /api/workspaces/sales-app/logs': { body: { lines: [], restarts: 3 } },
      'GET /api/workspaces/sales-app/steps/Started/log': {
        body: records({ kind: 'start', container: 'app' }, { kind: 'line', container: 'app', time: '2026-10-30T12:00:04Z', text: "ModuleNotFoundError: No module named 'plotly'" }),
      },
    })
    renderApp('/apps/sales-app')
    const steps = await screen.findByRole('list', { name: 'Startup steps' })
    const failed = within(steps).getAllByRole('listitem')[3]
    expect(await within(failed).findByText('Back-off restarting failed container app')).toBeTruthy()
    expect(await within(failed).findByText(logText("ModuleNotFoundError: No module named 'plotly'"))).toBeTruthy()
    expect(within(failed).getByRole('button', { name: /Hide events and log lines/ }).getAttribute('aria-expanded')).toBe('true')
  })

  it('shows what each done step recorded, long after its events are gone', async () => {
    const recorded: Record<string, string[]> = {
      Scheduled: ['node node-1', 'placed at 2026-10-31 10:07:12 UTC'],
      HomeReady: ['claim home-alice-1a2b, 10Gi, storage class standard', 'mounted within 2s of placement'],
      ImageReady: ['image ghcr.io/example/bokeh:3.9.2', 'digest sha256:4b1c', 'already on the node, not downloaded'],
      Started: ['container app started at 2026-10-31 10:07:15 UTC', 'restarts 0'],
      Ready: ['checked GET :8080/u/alice/sales-app/', 'answered 3s after the container started'],
    }
    const steps = allReady().map((s) => ({ ...s, evidence: recorded[s.type] }))
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Running', steps) },
      'GET /api/workspaces/sales-app/events': { body: { items: [] } },
      'GET /api/workspaces/sales-app/logs': { body: { lines: [], restarts: 0 } },
      'GET /api/workspaces/sales-app/steps/HomeReady/log': { body: '' },
      'GET /api/workspaces/sales-app/steps/Started/log': {
        body: records({ kind: 'line', container: 'app', time: '2026-10-31T10:07:18Z', text: '2026-10-31 10:07:18,494 Starting Bokeh server version 3.9.2 (running on Tornado 6.6)' }),
      },
    })
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('button', { name: /Started/ }))
    const items = within(screen.getByRole('list', { name: 'Startup steps' })).getAllByRole('listitem')
    for (const [i, type] of ['Scheduled', 'HomeReady', 'ImageReady', 'Started', 'Ready'].entries()) {
      await userEvent.click(within(items[i]).getByRole('button', { name: /Show events and log lines/ }))
      for (const line of recorded[type]) expect(within(items[i]).getByText(line)).toBeTruthy()
    }
    // The app's log belongs to starting the app, not to the answer check.
    const bokeh = logText('2026-10-31 10:07:18,494 Starting Bokeh server version 3.9.2 (running on Tornado 6.6)')
    expect(await within(items[3]).findByText(bokeh)).toBeTruthy()
    expect(within(items[4]).queryByText(bokeh)).toBeNull()
    expect(within(items[4]).queryByRole('region', { name: 'Output' })).toBeNull()
  })

  it('says what the answer check is waiting for while the app has not answered', async () => {
    const steps = stepsAt(4, 'Starting', ['checking TCP :8080, no answer yet'])
    detail(ws('sales-app', 'Sales app', 'Starting', steps))
    renderApp('/apps/sales-app')
    const items = within(await screen.findByRole('list', { name: 'Startup steps' })).getAllByRole('listitem')
    // The step in progress is open by itself.
    expect(within(items[4]).getByText('checking TCP :8080, no answer yet')).toBeTruthy()
  })

  it("keeps the running app's log folded until asked, then follows it live", async () => {
    detail(ws('sales-app', 'Sales app', 'Running', allReady()))
    renderApp('/apps/sales-app')
    const show = await screen.findByRole('button', { name: "Show the running app's log" })
    expect(show.getAttribute('aria-expanded')).toBe('false')
    // Folded: no stream is opened.
    expect(FakeEventSource.instances).toHaveLength(0)
    await userEvent.click(show)
    expect(screen.getByRole('heading', { name: "Running app's log" })).toBeTruthy()
    expect(FakeEventSource.latest().url).toBe('/api/workspaces/sales-app/logs/stream')
    await userEvent.click(screen.getByRole('button', { name: "Hide the running app's log" }))
    expect(screen.queryByRole('heading', { name: "Running app's log" })).toBeNull()
    expect(FakeEventSource.latest().closed).toBe(true)
  })

  const run = {
    image: 'ghcr.io/example/pixi:1',
    command: ['/opt/javo/pod-user.sh', 'streamlit', 'run', '/home/user/sales/app.py'],
    line: '/opt/javo/pod-user.sh streamlit run /home/user/sales/app.py',
    environment: "The template's environment streamlit",
  }

  it('shows the image, environment and command the app runs', async () => {
    detail(ws('sales-app', 'Sales app', 'Running', allReady(), { run }))
    renderApp('/apps/sales-app')
    const card = (await screen.findByText('What runs')).closest('.card') as HTMLElement
    expect(within(card).getByText(run.image)).toBeTruthy()
    expect(within(card).getByText(run.environment)).toBeTruthy()
    expect(card.querySelector('.cmdline code')?.textContent).toBe(run.line)
    expect(within(card).getByRole('button', { name: 'Copy command' })).toBeTruthy()
  })

  it("starts the running app's log with the command it started with", async () => {
    detail(ws('sales-app', 'Sales app', 'Running', allReady(), { run }))
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('button', { name: "Show the running app's log" }))
    const log = screen.getByRole('heading', { name: "Running app's log" }).closest('section') as HTMLElement
    const started = within(log).getByRole('group', { name: 'Started with' })
    expect(started.querySelector('code')?.textContent).toBe(run.line)
    expect(within(started).getByText(run.image)).toBeTruthy()
    // It stands above the lines, not among them.
    expect(started.compareDocumentPosition(within(log).getByRole('log')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })

  it('shows the API message for an app that does not exist', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/nope': { status: 404, body: { title: 'Not Found', detail: 'No app with that name.' } },
    })
    renderApp('/apps/nope')
    expect(await screen.findByText('No app with that name.')).toBeTruthy()
  })
})

describe('App size', () => {
  const running = ws('sales-app', 'Sales app', 'Running', allReady())
  function withSizes(put: object) {
    return mockApi({
      'GET /api/me': { body: me },
      'GET /api/sizes': { body: { items: sizes } },
      'GET /api/workspaces/sales-app': { body: running },
      'PUT /api/workspaces/sales-app/size': put,
    })
  }

  it('shows the size and changes it', async () => {
    const api = withSizes({ body: { ...running, size: 'medium' } })
    renderApp('/apps/sales-app')
    expect(await screen.findByText('Small · 1 CPU · 2 GB')).toBeTruthy()

    // The sizes show without a click; saving waits for Save size.
    expect(await screen.findByRole('radiogroup', { name: 'Size' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Change size' })).toBeNull()
    expect(screen.getByText(/restarts/)).toBeTruthy()
    expect((screen.getByRole('radio', { name: /Small/ }) as HTMLInputElement).checked).toBe(true)
    expect((screen.getByRole('button', { name: 'Save size' }) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(screen.getByRole('radio', { name: /Medium/ }))
    expect(api.calls.find((c) => c.method === 'PUT')).toBeUndefined()
    await userEvent.click(screen.getByRole('button', { name: 'Save size' }))

    expect(await screen.findByText('Medium · 4 CPU · 8 GB')).toBeTruthy()
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ size: 'medium' })
    expect((screen.getByRole('radio', { name: /Medium/ }) as HTMLInputElement).checked).toBe(true)
  })

  it('puts the sizes back to the current one on Cancel', async () => {
    withSizes({ body: running })
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('radio', { name: /Large/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect((screen.getByRole('radio', { name: /Small/ }) as HTMLInputElement).checked).toBe(true)
    expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull()
  })

  it('keeps the choice open and shows why when the change is refused', async () => {
    withSizes({
      status: 422,
      body: { status: 422, title: 'Invalid', errors: [{ location: 'body.size', message: 'Pick one of the sizes on the list.' }] },
    })
    renderApp('/apps/sales-app')
    await userEvent.click(await screen.findByRole('radio', { name: /Large/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Save size' }))
    expect(await screen.findByText('Pick one of the sizes on the list.')).toBeTruthy()
    expect(screen.getByRole('radiogroup', { name: 'Size' })).toBeTruthy()
  })

  it('shows a held app as information, not as a failure', async () => {
    detail(ws('sales-app', 'Sales app', 'Stopped', stepsAt(1, 'HomeMigrating')))
    renderApp('/apps/sales-app')
    const card = (await screen.findByText(REASONS.HomeMigrating.title)).closest('.card') as HTMLElement
    expect(card.className).toContain('info')
    expect(card.className).not.toContain('err')
    expect(within(card).getByText(REASONS.HomeMigrating.what)).toBeTruthy()
    expect(within(card).getByText(REASONS.HomeMigrating.todo)).toBeTruthy()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('names the folders that are not saved behind More, in the notice words', async () => {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Running', allReady()) },
      'GET /api/storage': { body: storageView({ notices: [NOTICES.ScratchPaths] as never }) },
    })
    renderApp('/apps/sales-app')
    const details = (await screen.findByText('What you asked for')).closest('.card') as HTMLElement
    expect(within(details).queryByText(NOTICES.ScratchPaths.text)).toBeNull()
    await userEvent.click(await within(details).findByRole('button', { name: 'Folders that are not saved' }))
    expect(within(details).getByText(NOTICES.ScratchPaths.text)).toBeTruthy()
    expect(within(details).getByText('~/.cache')).toBeTruthy()
    expect(within(details).getByText('/scratch')).toBeTruthy()
  })
})

describe('App at a custom amount', () => {
  const GiB = 2 ** 30
  const custom = ws('sales-app', 'Sales app', 'Running', allReady(), { size: '', resources: { cpu: 2.5, memory: 6 * GiB } })
  function withCustom() {
    return mockApi({
      'GET /api/me': { body: me },
      'GET /api/sizes': { body: { items: sizes, custom: allowance } },
      'GET /api/workspaces/sales-app': { body: custom },
      'PUT /api/workspaces/sales-app/size': { body: custom },
    })
  }

  it('shows the amount and changes it', async () => {
    const api = withCustom()
    renderApp('/apps/sales-app')
    expect(await screen.findByText('Custom · 2.5 CPU · 6 GB')).toBeTruthy()
    const cpu = (await screen.findByRole('spinbutton', { name: 'CPU' })) as HTMLInputElement
    expect(cpu.value).toBe('2.5')
    await userEvent.clear(cpu)
    await userEvent.type(cpu, '3')
    await userEvent.click(screen.getByRole('button', { name: 'Save size' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ resources: { cpu: 3, memory: 6 * GiB } }))
  })

  it('moves back to a size from the list', async () => {
    const api = withCustom()
    renderApp('/apps/sales-app')
    expect(((await screen.findByRole('radio', { name: /Custom/ })) as HTMLInputElement).checked).toBe(true)
    await userEvent.click(screen.getByRole('radio', { name: /Medium/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Save size' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ size: 'medium' }))
  })
})

describe('App at a size no machine can hold', () => {
  const stuck = ws('sales-app', 'Sales app', 'Failed', stepsAt(0, 'SizeTooBig', ['needs 8 CPU and 32 GB; the largest machine has 8 CPU and 15.6 GB']), {
    size: 'large',
  })

  it('shows the failure, not a wait, and its Change size opens the sizes', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/sizes': { body: { items: singleNodeSizes } },
      'GET /api/workspaces/sales-app': { body: stuck },
      'PUT /api/workspaces/sales-app/size': { body: { ...stuck, size: 'medium', phase: 'Starting', steps: stepsAt(0, 'Starting') } },
    })
    renderApp('/apps/sales-app')
    const alert = await screen.findByRole('alert')
    expect(within(alert).getByText(REASONS.SizeTooBig.title)).toBeTruthy()
    expect(screen.queryByText(REASONS.NoCapacity.title)).toBeNull()

    await userEvent.click(within(alert).getByRole('button', { name: 'Change size' }))
    const large = screen.getByRole('radio', { name: /Large/ }) as HTMLInputElement
    expect(large.disabled).toBe(true)
    expect(screen.getByText(TOO_BIG)).toBeTruthy()
    await userEvent.click(screen.getByRole('radio', { name: /Medium/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Save size' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ size: 'medium' }))
  })
})

describe('What an app runs', () => {
  function withApp(app: object, put: object) {
    return mockApi({
      'GET /api/me': { body: me },
      'GET /api/sizes': { body: { items: [] } },
      'GET /api/templates': { body: { items: [...templates, customTemplate, containerTemplate] } },
      'GET /api/workspaces/clock': { body: app },
      'PUT /api/workspaces/clock/launch': put,
    })
  }
  const custom = ws('clock', 'Clock', 'Running', allReady(), { template: 'custom', kind: 'custom', file: '', command: 'python a.py --port={{.Port}}' })
  const image = ws('clock', 'Clock', 'Running', allReady(), { template: 'container', kind: 'container', file: '', image: 'nginxinc/nginx-unprivileged:1.27', port: 8080 })

  it('shows the command and changes it', async () => {
    const changed = { ...custom, command: 'python b.py --port={{.Port}}' }
    const api = withApp(custom, { body: changed })
    renderApp('/apps/clock')
    expect(await screen.findByText('python a.py --port={{.Port}}')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Change command' }))
    expect(screen.getByText(/restarts/)).toBeTruthy()
    const field = screen.getByLabelText(/^Command/) as HTMLInputElement
    expect(field.value).toBe('python a.py --port={{.Port}}')
    await userEvent.clear(field)
    await userEvent.paste('python b.py --port={{.Port}}')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('python b.py --port={{.Port}}')).toBeTruthy()
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ command: 'python b.py --port={{.Port}}' })
    expect(screen.queryByLabelText(/^Command/)).toBeNull()
  })

  it('shows the image, port and command and changes them together', async () => {
    const api = withApp(image, { body: { ...image, image: 'ghcr.io/example/web:2', port: 9000 } })
    renderApp('/apps/clock')
    expect(await screen.findByText('nginxinc/nginx-unprivileged:1.27')).toBeTruthy()
    expect(screen.getByText('8080')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Change image' }))
    const img = screen.getByLabelText('Image') as HTMLInputElement
    await userEvent.clear(img)
    await userEvent.type(img, 'ghcr.io/example/web:2')
    const port = screen.getByLabelText('Port') as HTMLInputElement
    await userEvent.clear(port)
    await userEvent.type(port, '9000')
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText('ghcr.io/example/web:2')).toBeTruthy()
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ image: 'ghcr.io/example/web:2', port: 9000 })
  })

  it('keeps the fields open and shows why when the change is refused', async () => {
    const msg = 'Images from docker.io are not allowed here. Use an image from ghcr.io/example.'
    withApp(image, { status: 422, body: { status: 422, title: 'Invalid', errors: [{ location: 'body.image', message: msg }] } })
    renderApp('/apps/clock')
    await userEvent.click(await screen.findByRole('button', { name: 'Change image' }))
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(msg)).toBeTruthy()
    expect((screen.getByLabelText('Image') as HTMLInputElement).getAttribute('aria-invalid')).toBe('true')
  })
})

describe('App page of an app from Git', () => {
  const site = repoApp('site')
  const mocks = (app: object, extra: Parameters<typeof mockApi>[0] = {}) =>
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/templates': { body: { items: [staticTemplate] } },
      [`GET /api/workspaces/${(app as { name: string }).name}`]: { body: app },
      'GET /api/workspaces/site/previews': { body: { enabled: false, max: 3, forks: false, forksWarning: 'Forks run code you have not reviewed.', items: [] } },
      'GET /api/workspaces': { body: { items: [] } },
      ...extra,
    })

  it('shows the source card and, when previews are available, the previews card', async () => {
    mocks(site)
    renderApp('/apps/site')
    expect(await screen.findByRole('link', { name: 'github.com/example-org/site' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Redeploy' })).toBeTruthy()
    expect(await screen.findByRole('switch', { name: 'Preview open pull requests' })).toBeTruthy()
  })

  it('shows no previews card where previews are not available', async () => {
    const app = repoApp('site')
    mocks({ ...app, source: { ...app.source!, previewsAvailable: false } })
    renderApp('/apps/site')
    await screen.findByRole('link', { name: 'github.com/example-org/site' })
    expect(screen.queryByRole('switch', { name: 'Preview open pull requests' })).toBeNull()
  })

  it('shows a preview as managed by its app: a way back, Redeploy, and nothing to edit', async () => {
    const child = previewApp('site', 12)
    mocks(child, { 'GET /api/workspaces/site--pr-12': { body: child }, 'GET /api/workspaces/site': { body: repoApp('site') } })
    renderApp('/apps/site--pr-12')
    expect(await screen.findByText(/Preview of/)).toBeTruthy()
    const back = await screen.findByRole('link', { name: 'Site' })
    expect(back.getAttribute('href')).toBe('/apps/site')
    expect(screen.getByText(/pull request #12/)).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Redeploy' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Delete/ })).toBeNull()
    expect(screen.queryByRole('radiogroup', { name: 'Size' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Change API access' })).toBeNull()
    expect(screen.queryByRole('tab', { name: 'Sharing' })).toBeNull()
    expect(screen.queryByRole('switch', { name: 'Preview open pull requests' })).toBeNull()
    expect(screen.queryByText(/access token/)).toBeNull()
  })

  it('shows Redeploy for a static app from a home file', async () => {
    const page = ws('page', 'Page', 'Running', allReady(), { template: 'static-site', kind: 'static', file: 'index.html', redeployable: true })
    mocks(page)
    renderApp('/apps/page')
    expect(await screen.findByRole('button', { name: 'Redeploy' })).toBeTruthy()
  })
})

describe('The file on the app page', () => {
  it('shows the full path the app opens, inside the app', async () => {
    const app = ws('sales-app', 'Sales app', 'Running', allReady(), { file: 'sales/app.py', filePath: '/home/user/sales/app.py' })
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces/sales-app': { body: app } })
    renderApp('/apps/sales-app')
    expect(await screen.findByText('/home/user/sales/app.py')).toBeTruthy()
  })
})
