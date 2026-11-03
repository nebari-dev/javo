import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { StepLogRecord, Workspace } from '../../api'
import { mockApi, renderApp } from '../../test/api'
import { allReady, me, stepsAt, withSince, ws } from '../../test/fixtures'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

/** The steps of an app that installs an environment before it starts. */
const ENV_TYPES = ['Scheduled', 'HomeReady', 'ImageReady', 'EnvReady', 'Started', 'Ready']

/** A step log the server keeps open and writes records to, one JSON per line. */
function openStream() {
  let ctl!: ReadableStreamDefaultController<Uint8Array>
  const body = new ReadableStream<Uint8Array>({ start: (c) => void (ctl = c) })
  const enc = new TextEncoder()
  return {
    body,
    send: (...rs: StepLogRecord[]) => ctl.enqueue(enc.encode(rs.map((r) => JSON.stringify(r) + '\n').join(''))),
    raw: (s: string) => ctl.enqueue(enc.encode(s)),
    end: () => ctl.close(),
  }
}

const line = (container: string, text: string): StepLogRecord => ({ kind: 'line', container, text, time: '2026-11-02T12:00:00Z' })

/** The text of the log lines in el, in order. */
const linesIn = (el: HTMLElement) => Array.from(el.querySelectorAll('.log-text')).map((e) => e.textContent)

function stepItem(i: number) {
  return within(screen.getByRole('list', { name: 'Startup steps' })).getAllByRole('listitem')[i]
}

describe('Live startup steps', () => {
  it('opens the running step and appends its output as the server writes it', async () => {
    const env = openStream()
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Starting', stepsAt(3, 'Starting', [], ENV_TYPES)) },
      'GET /api/workspaces/sales-app/events': { body: { items: [] } },
      'GET /api/workspaces/sales-app/steps/EnvReady/log': { body: env.body },
    })
    renderApp('/apps/sales-app')
    await screen.findByRole('list', { name: 'Startup steps' })
    const item = stepItem(3)
    expect(within(item).getByText('Set up the environment')).toBeTruthy()
    expect(within(item).getByRole('button', { name: /Hide events and log lines/ }).getAttribute('aria-expanded')).toBe('true')

    env.send({ kind: 'start', container: 'env-install' }, line('env-install', 'Installing the template\'s environment'))
    await waitFor(() => expect(linesIn(item)).toEqual(['env-install', "Installing the template's environment"]))
    // A record split across chunks shows once it is whole.
    env.raw(JSON.stringify(line('env-install', 'pixi install: 41 packages')).slice(0, 20))
    env.raw(JSON.stringify(line('env-install', 'pixi install: 41 packages')).slice(20) + '\n')
    await waitFor(() => expect(linesIn(item)).toEqual(['env-install', "Installing the template's environment", 'pixi install: 41 packages']))
    expect(api.count('GET', '/api/workspaces/sales-app/steps/EnvReady/log')).toBe(1)
    // Steps not reached yet open no stream.
    expect(api.count('GET', '/api/workspaces/sales-app/steps/Started/log')).toBe(0)
  })

  it('shows a failed container with its exit and termination message, and a restart', async () => {
    const app = openStream()
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Failed', stepsAt(3, 'AppCrashed')) },
      'GET /api/workspaces/sales-app/events': { body: { items: [] } },
      'GET /api/workspaces/sales-app/steps/Started/log': { body: app.body },
    })
    renderApp('/apps/sales-app')
    await screen.findByRole('list', { name: 'Startup steps' })
    app.send(
      { kind: 'start', container: 'app' },
      line('app', "ModuleNotFoundError: No module named 'plotly'"),
      { kind: 'exit', container: 'app', exitCode: 1, reason: 'Error' },
      { kind: 'restart', container: 'app', restarts: 1 },
      line('app', 'Collecting usage statistics.'),
    )
    const item = stepItem(3)
    await waitFor(() =>
      expect(linesIn(item)).toEqual(['app', "ModuleNotFoundError: No module named 'plotly'", 'app exited with code 1 (Error)', 'app restarted (restart 1)', 'Collecting usage statistics.']),
    )
  })

  it('follows the app log in the start step while the app is being checked', async () => {
    const app = openStream()
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Starting', stepsAt(4, 'Starting')) },
      'GET /api/workspaces/sales-app/events': { body: { items: [] } },
      'GET /api/workspaces/sales-app/steps/Started/log': { body: app.body },
    })
    renderApp('/apps/sales-app')
    await screen.findByRole('list', { name: 'Startup steps' })
    app.send({ kind: 'start', container: 'app' }, line('app', '[I 2026-11-02 12:00:01.500 ServerApp] Jupyter Server 2.17.0 is running at:'))
    await waitFor(() => expect(linesIn(stepItem(3))).toContain('[I 2026-11-02 12:00:01.500 ServerApp] Jupyter Server 2.17.0 is running at:'))
  })

  it('keeps a finished step\'s whole output: read once when opened, not followed again', async () => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Starting', stepsAt(4, 'Starting', [], ENV_TYPES)) },
      'GET /api/workspaces/sales-app/events': { body: { items: [] } },
      'GET /api/workspaces/sales-app/steps/EnvReady/log': {
        body: [{ kind: 'start', container: 'env-install' }, line('env-install', 'done installing'), { kind: 'exit', container: 'env-install', exitCode: 0, reason: 'Completed' }]
          .map((r) => JSON.stringify(r))
          .join('\n') + '\n',
      },
      'GET /api/workspaces/sales-app/steps/Started/log': { body: '' },
    })
    renderApp('/apps/sales-app')
    await screen.findByRole('list', { name: 'Startup steps' })
    const item = stepItem(3)
    expect(api.count('GET', '/api/workspaces/sales-app/steps/EnvReady/log')).toBe(0)
    await userEvent.click(within(item).getByRole('button', { name: /Show events and log lines/ }))
    await waitFor(() => expect(linesIn(item)).toEqual(['env-install', 'done installing', 'env-install exited with code 0 (Completed)']))
    await new Promise((r) => setTimeout(r, 1500))
    expect(api.count('GET', '/api/workspaces/sales-app/steps/EnvReady/log')).toBe(1)
  })
})

describe('Reaching Running', () => {
  const starting = ws('sales-app', 'Sales app', 'Starting', stepsAt(4, 'Starting'))
  const running: Workspace = ws('sales-app', 'Sales app', 'Running', withSince(allReady(), '2026-10-30T12:00:00Z', [0, 1, 30, 31, 42]))

  const address = () => new URL('/u/alice/sales-app/', window.location.href).href

  function startThenRun() {
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': [{ body: starting }, { body: running }],
      'GET /api/workspaces/sales-app/events': { body: { items: [] } },
      'GET /api/workspaces/sales-app/steps/Started/log': { body: '' },
    })
    renderApp('/apps/sales-app')
  }

  it('shows every step done, then folds the steps into the start time, and says the app is ready', async () => {
    startThenRun()
    await screen.findByRole('list', { name: 'Startup steps' })
    // The page polls while the app starts; the next answer is Running.
    const ready = (await screen.findByText('Your app is ready', {}, { timeout: 4000 })).closest('.banner') as HTMLElement
    expect(within(ready).getByText(address())).toBeTruthy()
    const openApp = within(ready).getByRole('link', { name: /Open app/ })
    expect(openApp.getAttribute('href')).toBe('/u/alice/sales-app/')
    expect(openApp.getAttribute('target')).toBe('_blank')
    // No highlight on Open and no arrow.
    expect(screen.getByRole('link', { name: /^Open$/ }).classList.contains('attention')).toBe(false)
    expect(document.querySelector('.pointer-arrow')).toBeNull()

    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    await userEvent.click(within(ready).getByRole('button', { name: 'Copy link' }))
    expect(writeText).toHaveBeenCalledWith(address())
    expect(await within(ready).findByRole('button', { name: 'Copied' })).toBeTruthy()

    const steps = screen.getByRole('list', { name: 'Startup steps' })
    expect(within(steps).getAllByRole('listitem').map((i) => i.dataset.state)).toEqual(['done', 'done', 'done', 'done', 'done'])
    expect(screen.getByRole('button', { name: 'Started in 42 s: hide the startup steps' })).toBeTruthy()

    await waitFor(() => expect(screen.queryByRole('list', { name: 'Startup steps' })).toBeNull(), { timeout: 8000 })
    expect(screen.getByRole('button', { name: 'Started in 42 s: show the startup steps' })).toBeTruthy()
    // The ready card stays after the steps fold.
    expect(screen.getByText('Your app is ready')).toBeTruthy()
  }, 15000)

  it('hides the ready card when it is dismissed', async () => {
    startThenRun()
    await screen.findByText('Your app is ready', {}, { timeout: 4000 })
    await userEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('Your app is ready')).toBeNull()
    expect(screen.getByRole('link', { name: /^Open$/ })).toBeTruthy()
  }, 8000)

  it('shows no ready card or steps for an app already running when the page opens', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces/sales-app': { body: running } })
    renderApp('/apps/sales-app')
    const open = await screen.findByRole('link', { name: /Open/ })
    expect(open.classList.contains('attention')).toBe(false)
    expect(screen.queryByText('Your app is ready')).toBeNull()
    expect(document.querySelector('.pointer-arrow')).toBeNull()
    expect(screen.queryByRole('list', { name: 'Startup steps' })).toBeNull()
    await act(async () => {})
  })
})

describe('Moving the home', () => {
  it('follows the copy of the home while it moves to another storage mode', async () => {
    const copy = openStream()
    mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces/sales-app': { body: ws('sales-app', 'Sales app', 'Stopped', stepsAt(1, 'HomeMigrating')) },
      'GET /api/workspaces/sales-app/steps/HomeReady/log': { body: copy.body },
    })
    renderApp('/apps/sales-app')
    copy.send({ kind: 'start', container: 'copy' }, line('copy', 'copied 1200 of 5000 files'))
    const out = await screen.findByRole('region', { name: 'Output' })
    await waitFor(() => expect(linesIn(out)).toEqual(['copy', 'copied 1200 of 5000 files']))
  })
})
