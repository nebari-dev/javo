import { QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { makeQueryClient } from '../../lib/queries'
import { location, mockApi, renderApp, type Reply } from '../../test/api'
import { RunLog } from './RunLog'
import { customJobTemplate, engines, failureProblem, jobTemplate, me, printTimeDetail, templates, weeklyDetail, weeklyView } from '../../test/fixtures'
import { choose, optionsOf } from '../../test/select'

afterEach(() => {
  vi.unstubAllGlobals()
})

const home: Reply = {
  body: {
    path: '',
    entries: [{ name: 'reports', kind: 'folder', size: 0, modified: '2026-10-29T10:00:00Z' }],
  },
}
const reports: Reply = {
  body: {
    path: 'reports',
    entries: [
      { name: 'sales.ipynb', kind: 'file', size: 4096, modified: '2026-10-30T11:02:00Z' },
      { name: 'notes.txt', kind: 'file', size: 10, modified: '2026-10-30T11:02:00Z' },
    ],
  },
}

// The job template without its sample job, so a new job starts blank.
const plainJobTemplate = { ...jobTemplate, jobs: { ...jobTemplate.jobs!, sample: undefined } }

function base(extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items: [...templates, plainJobTemplate] } },
    'GET /api/engines': { body: { items: engines } },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/jobs': { body: { items: [weeklyView] } },
    'GET /api/jobs/weekly-report': { body: weeklyDetail },
    'GET /api/files': (c) => (new URL(c.path, 'http://x').searchParams.get('path') === 'reports' ? reports : home),
    ...extra,
  })
}

describe('Jobs list', () => {
  it('shows a job with a 12 cell run strip that names the failed count', async () => {
    base()
    renderApp('/jobs')
    expect((await screen.findByRole('link', { name: 'Weekly report' })).getAttribute('href')).toBe('/jobs/weekly-report')
    const strip = screen.getByRole('img', { name: '10 succeeded, 2 failed' })
    expect(strip.querySelectorAll('.run-cell')).toHaveLength(12)
    expect(screen.getByText('reports/weekly.ipynb')).toBeTruthy()
    // On the reader's clock: 24 or 12 hour by locale.
    expect(screen.getByText(/At 06:00( AM)?, only on Monday/)).toBeTruthy()
    expect(screen.getByText('Europe/Berlin')).toBeTruthy()
  })

  it('says what a job is when there are none', async () => {
    base({ 'GET /api/jobs': { body: { items: [] } } })
    renderApp('/jobs')
    expect(await screen.findByText('No jobs yet')).toBeTruthy()
    expect(screen.getByText('Run a script or notebook in the background, once or on a schedule.')).toBeTruthy()
    // One New job control, in the page header.
    expect(screen.queryAllByRole('link', { name: /New job/ }).length + screen.queryAllByRole('button', { name: /New job/ }).length).toBe(1)
  })

  it('shows a command job by its command', async () => {
    base({ 'GET /api/jobs': { body: { items: [printTimeDetail] } } })
    renderApp('/jobs')
    await screen.findByRole('link', { name: 'Print the time' })
    expect(screen.getByText('bash /opt/javo/samples/jobs/print-time.sh')).toBeTruthy()
  })

  it('opens New job filled in from a sample, and makes nothing until Create job', async () => {
    const api = base({
      // Another job template comes first, so the form starts on the sample's only when the sample picks it.
      'GET /api/templates': { body: { items: [...templates, customJobTemplate, jobTemplate] } },
      'GET /api/jobs': { body: { items: [] } },
      'POST /api/jobs': { status: 201, body: printTimeDetail },
      'POST /api/jobs/print-the-time/run': { status: 201, body: { ...weeklyDetail.lastRun, id: 'run-1', outcome: 'Running' } },
      'GET /api/jobs/print-the-time': { body: printTimeDetail },
    })
    renderApp('/jobs')
    expect(await screen.findByText('A ready-made job to start from')).toBeTruthy()
    await userEvent.click(await screen.findByRole('button', { name: /Try a sample job: Print the time/ }))
    await waitFor(() => expect(location()).toBe('/jobs/new?sample=python-jobs'))
    expect(await screen.findByRole('heading', { name: 'New job' })).toBeTruthy()
    expect(screen.getByLabelText('Template').textContent).toBe('Python jobs')
    expect((screen.getByLabelText('Command') as HTMLInputElement).value).toBe('bash /opt/javo/samples/jobs/print-time.sh')
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Print the time')
    expect((screen.getByLabelText('Parameter 1 name') as HTMLInputElement).value).toBe('greeting')
    expect((screen.getByLabelText('Parameter 1 value') as HTMLInputElement).value).toBe('hello')
    expect((screen.getByRole('radio', { name: /Run once/ }) as HTMLInputElement).checked).toBe(true)
    // Nothing is made or run by the click on the card.
    expect(api.calls.filter((c) => c.method !== 'GET')).toHaveLength(0)

    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/print-the-time'))
    const body = api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body as Record<string, unknown>
    expect(body).toMatchObject({
      displayName: 'Print the time',
      template: 'python-jobs',
      engine: 'command',
      command: 'bash /opt/javo/samples/jobs/print-time.sh',
      parameters: [{ name: 'greeting', type: 'string', default: 'hello' }],
    })
    expect(body.schedule).toBeUndefined()
    expect(api.count('POST', '/api/jobs/print-the-time/run')).toBe(1)
  })

  it("names a sample's job with the next free number when its name is taken", async () => {
    base({
      'GET /api/templates': { body: { items: [...templates, jobTemplate] } },
      'GET /api/jobs': { body: { items: [{ ...weeklyView, name: 'print-the-time', displayName: 'Print the time' }] } },
    })
    renderApp('/jobs/new?sample=python-jobs')
    expect(await screen.findByRole('heading', { name: 'New job' })).toBeTruthy()
    await waitFor(() => expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Print the time 2'))
  })

  it('asks again every 2 s while a run is going', async () => {
    const running = { ...weeklyView, recent: ['Running'], lastRun: { ...weeklyView.lastRun, outcome: 'Running' }, problem: undefined }
    const api = base({ 'GET /api/jobs': { body: { items: [running] } } })
    renderApp('/jobs')
    await screen.findByRole('link', { name: 'Weekly report' })
    await waitFor(() => expect(api.count('GET', '/api/jobs')).toBeGreaterThan(1), { timeout: 4000 })
  })
})

describe('Job detail', () => {
  it('shows the command of a command job where a file job shows its file', async () => {
    base({ 'GET /api/jobs/print-the-time': { body: printTimeDetail } })
    renderApp('/jobs/print-the-time')
    const term = await screen.findByText('Command', { selector: 'dt' })
    expect(term.nextElementSibling?.textContent).toBe('bash /opt/javo/samples/jobs/print-time.sh')
    expect(screen.queryByText('Source', { selector: 'dt' })).toBeNull()
  })

  it('keeps retries, the time limit and notifications behind a toggle that names them', async () => {
    base()
    renderApp('/jobs/weekly-report')
    await screen.findByRole('heading', { name: 'Weekly report' })
    expect(screen.queryByText('Retries')).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Retries, time limit and notifications' }))
    expect(screen.getByText('Retries')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Hide retries, time limit and notifications' })).toBeTruthy()
  })

  it('keeps the new job options behind a toggle that names them, with the overlap rule only for a schedule', async () => {
    base()
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('button', { name: 'Retries and time limit' }))
    expect(screen.getByLabelText('Retries')).toBeTruthy()
    expect(screen.queryByLabelText('If a run is still going when the next is due')).toBeNull()
    await userEvent.click(screen.getByRole('radio', { name: /On a schedule/ }))
    expect(screen.getByRole('button', { name: 'Hide overlap rule, retries and time limit' })).toBeTruthy()
    expect(screen.getByLabelText('If a run is still going when the next is due')).toBeTruthy()
  })

  it('shows the failure in plain words and keeps who can help behind Show more', async () => {
    base()
    renderApp('/jobs/weekly-report')
    expect(await screen.findByRole('heading', { name: 'A notebook cell raised an error' })).toBeTruthy()
    expect(screen.getByText("Cell 14 raised KeyError: 'region'")).toBeTruthy()
    expect(screen.queryByText('Who can help', { exact: true })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: /Who can help/ }))
    expect(screen.getByText('Who can help', { exact: true })).toBeTruthy()
  })

  it('keeps the old run message and the re-run button off a problem with the job itself', async () => {
    const problem = { ...failureProblem, reason: 'TemplateNotAllowed', title: 'This template is no longer allowed', evidence: [] }
    base({ 'GET /api/jobs/weekly-report': { body: { ...weeklyDetail, phase: 'Blocked', problem } } })
    renderApp('/jobs/weekly-report')
    expect(await screen.findByRole('heading', { name: 'This template is no longer allowed' })).toBeTruthy()
    expect(screen.queryByText("Cell 14 raised KeyError: 'region'")).toBeNull()
    expect(screen.queryByRole('button', { name: 'Re-run with same inputs' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Open executed notebook' })).toBeNull()
  })

  it('re-runs with the inputs of the failed run, not the saved defaults', async () => {
    const api = base({ 'POST /api/jobs/weekly-report/run': { status: 201, body: { ...weeklyView.lastRun, id: 'run-4', outcome: 'Running' } } })
    renderApp('/jobs/weekly-report')
    await userEvent.click(await screen.findByRole('button', { name: 'Re-run with same inputs' }))
    await waitFor(() => expect(api.count('POST', '/api/jobs/weekly-report/run')).toBe(1))
    const call = api.calls.find((c) => c.method === 'POST')
    expect(call?.body).toEqual({ parameters: { region: 'apac' } })
  })

  it('does not re-run a job that has inputs with none when the run kept no inputs', async () => {
    const lastRun = { ...weeklyDetail.lastRun, parameters: undefined }
    const api = base({ 'GET /api/jobs/weekly-report': { body: { ...weeklyDetail, lastRun } } })
    renderApp('/jobs/weekly-report')
    const rerun = await screen.findByRole('button', { name: 'Re-run with same inputs' })
    expect((rerun as HTMLButtonElement).disabled).toBe(true)
    expect(screen.getByText('This run did not keep its inputs.')).toBeTruthy()
    await userEvent.click(rerun)
    expect(api.count('POST', '/api/jobs/weekly-report/run')).toBe(0)
  })

  it('opens the executed notebook in the file viewer, which frames the HTML in a sandbox', async () => {
    base()
    renderApp('/jobs/weekly-report')
    const link = await screen.findByRole('link', { name: 'Open executed notebook' })
    expect(link.getAttribute('href')).toBe('/files/view?path=jobs%2Fweekly-report%2Frun-3%2Foutput.html&show=page')
  })

  it('links each output file of a run, never the run folder', async () => {
    base()
    renderApp('/jobs/weekly-report')
    const runs = await screen.findByRole('table', { name: 'Runs of Weekly report' })
    const hrefs = within(runs).getAllByRole('link').map((a) => a.getAttribute('href'))
    expect(hrefs).toContain('/files/view?path=jobs%2Fweekly-report%2Frun-2%2Foutput.html&show=page')
    expect(hrefs).not.toContain('/api/files/raw?path=jobs%2Fweekly-report%2Frun-2')
  })

  it('offers no output link when the run made no files', async () => {
    base({ 'GET /api/jobs/weekly-report': { body: { ...weeklyDetail, lastRun: { ...weeklyDetail.lastRun, outputs: [] } } } })
    renderApp('/jobs/weekly-report')
    await screen.findByRole('button', { name: 'Re-run with same inputs' })
    expect(screen.queryByRole('link', { name: /^Open / })).toBeNull()
  })

  it('shows the missed-run notice before resuming and resumes only after confirming', async () => {
    const paused = {
      ...weeklyDetail,
      phase: 'Paused',
      schedule: { ...weeklyDetail.schedule, paused: true },
      resumeNotice: 'Resuming starts one missed run now.',
    }
    const api = base({
      'GET /api/jobs/weekly-report': { body: paused },
      'POST /api/jobs/weekly-report/resume': { body: weeklyDetail },
    })
    renderApp('/jobs/weekly-report')
    await userEvent.click(await screen.findByRole('button', { name: 'Resume schedule' }))
    expect(await screen.findByText('Resuming starts one missed run now.')).toBeTruthy()
    expect(api.count('POST', '/api/jobs/weekly-report/resume')).toBe(0)
    await userEvent.click(screen.getByRole('button', { name: 'Resume anyway' }))
    await waitFor(() => expect(api.count('POST', '/api/jobs/weekly-report/resume')).toBe(1))
  })

  it('resumes at once when no run was missed', async () => {
    const paused = { ...weeklyDetail, phase: 'Paused', schedule: { ...weeklyDetail.schedule, paused: true } }
    const api = base({
      'GET /api/jobs/weekly-report': { body: paused },
      'POST /api/jobs/weekly-report/resume': { body: weeklyDetail },
    })
    renderApp('/jobs/weekly-report')
    await userEvent.click(await screen.findByRole('button', { name: 'Resume schedule' }))
    await waitFor(() => expect(api.count('POST', '/api/jobs/weekly-report/resume')).toBe(1))
  })
})

describe('Run log', () => {
  it('reads the log once more when the run ends, so the last output is not lost', async () => {
    let text = 'working'
    const api = mockApi({ 'GET /api/jobs/weekly-report/runs/run-4/log': () => ({ body: text }) })
    const client = makeQueryClient()
    const view = (running: boolean) => (
      <QueryClientProvider client={client}>
        <RunLog job="weekly-report" run="run-4" running={running} />
      </QueryClientProvider>
    )
    const { rerender } = render(view(true))
    await screen.findByText('working')
    text = "working\nKeyError: 'region'"
    rerender(view(false))
    await waitFor(() => expect(screen.getAllByRole('listitem').map((li) => li.querySelector('.log-text')?.textContent)).toEqual(['working', "KeyError: 'region'"]))
    expect(screen.getAllByRole('listitem')[1].dataset.level).toBe('error')
    expect(api.count('GET', '/api/jobs/weekly-report/runs/run-4/log')).toBe(2)
  })

  // The server keeps a running run's log open and writes each line as the
  // container prints it; the response only ends when the run does.
  function openLog() {
    let ctl!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({ start: (c) => void (ctl = c) })
    const enc = new TextEncoder()
    return { body, print: (s: string) => ctl.enqueue(enc.encode(s)), end: () => ctl.close() }
  }
  const logLines = () => screen.getAllByRole('listitem').map((li) => li.querySelector('.log-text')?.textContent)

  it('shows each line while the run goes, before the run ends', async () => {
    const log = openLog()
    mockApi({ 'GET /api/jobs/weekly-report/runs/run-4/log': { body: log.body, headers: { 'X-Log-Source': 'live' } } })
    render(
      <QueryClientProvider client={makeQueryClient()}>
        <RunLog job="weekly-report" run="run-4" running />
      </QueryClientProvider>,
    )
    log.print('step 1\n')
    await waitFor(() => expect(logLines()).toEqual(['step 1']))
    log.print('step 2\n')
    await waitFor(() => expect(logLines()).toEqual(['step 1', 'step 2']))
    expect(screen.getByText('Live')).toBeTruthy()
  })

  it('opens the log as soon as the container starts, and keeps the whole log when the run ends', async () => {
    const log = openLog()
    let ended = false
    const api = mockApi({
      'GET /api/jobs/weekly-report/runs/run-4/log': [
        { status: 409, body: { title: 'The run has not started yet.' }, headers: { 'Retry-After': '2' } },
        { body: log.body, headers: { 'X-Log-Source': 'live' } },
        () => ({ body: ended ? 'step 1\nstep 2\n' : 'unexpected' }),
      ],
    })
    const client = makeQueryClient()
    const view = (running: boolean) => (
      <QueryClientProvider client={client}>
        <RunLog job="weekly-report" run="run-4" running={running} />
      </QueryClientProvider>
    )
    const { rerender } = render(view(true))
    await waitFor(() => expect(api.count('GET', '/api/jobs/weekly-report/runs/run-4/log')).toBe(2), { timeout: 3000 })
    log.print('step 1\n')
    await waitFor(() => expect(logLines()).toEqual(['step 1']))
    log.print('step 2\n')
    log.end()
    ended = true
    rerender(view(false))
    await waitFor(() => expect(api.count('GET', '/api/jobs/weekly-report/runs/run-4/log')).toBe(3))
    await waitFor(() => expect(logLines()).toEqual(['step 1', 'step 2']))
  })
})

describe('Run log on the job page', () => {
  it('keeps the log of the run it opened by itself on screen when that run ends', async () => {
    const going = { ...weeklyDetail.lastRun, id: 'run-4', outcome: 'Running', title: undefined, finishTime: undefined }
    const ended = { ...going, outcome: 'Succeeded', finishTime: '2026-11-02T05:01:00Z' }
    const api = base({
      'GET /api/jobs/weekly-report': [{ body: { ...weeklyDetail, runs: [going, ...weeklyDetail.runs] } }, { body: { ...weeklyDetail, runs: [ended, ...weeklyDetail.runs] } }],
      'GET /api/jobs/weekly-report/runs/run-4/log': { body: 'all done\n' },
    })
    renderApp('/jobs/weekly-report')
    expect(await screen.findByText('all done')).toBeTruthy()
    await waitFor(() => expect(api.count('GET', '/api/jobs/weekly-report')).toBe(2), { timeout: 4000 })
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Cancel' })).toBeNull())
    await waitFor(() => expect(screen.getByRole('list', { name: 'Run log' })).toBeTruthy())
    expect(screen.getByText('all done')).toBeTruthy()
  })
})

describe('Run log fallback', () => {
  const view = (running: boolean) => (
    <QueryClientProvider client={makeQueryClient()}>
      <RunLog job="print-the-time" run="run-9" running={running} />
    </QueryClientProvider>
  )

  it('shows the log saved in the run folder once the run is gone, like a live log', async () => {
    mockApi({ 'GET /api/jobs/print-the-time/runs/run-9/log': { body: 'The time is 06:00\n', headers: { 'X-Log-Source': 'saved' } } })
    render(view(false))
    const list = await screen.findByRole('list', { name: 'Run log' })
    expect(within(list).getAllByRole('listitem').map((li) => li.querySelector('.log-text')?.textContent)).toEqual(['The time is 06:00'])
    expect(screen.getByText('Saved log')).toBeTruthy()
    expect(screen.queryByText('Live')).toBeNull()
  })

  it('says it is live while the run goes', async () => {
    mockApi({ 'GET /api/jobs/print-the-time/runs/run-9/log': { body: 'working', headers: { 'X-Log-Source': 'live' } } })
    render(view(true))
    await screen.findByRole('list', { name: 'Run log' })
    expect(screen.getByText('Live')).toBeTruthy()
  })

  it('says in plain words why there is no log when neither the run nor its saved log is left', async () => {
    const why = "This run's log is gone. The run may have ended before it printed anything, or its log file was deleted from your home folder."
    mockApi({ 'GET /api/jobs/print-the-time/runs/run-9/log': { status: 410, body: { title: why } } })
    render(view(false))
    expect(await screen.findByText(why)).toBeTruthy()
    expect(screen.getByText('No log to show')).toBeTruthy()
  })
})

const cronField = () => screen.getByLabelText('Cron') as HTMLInputElement

describe('New job', () => {
  async function chooseNotebook() {
    await userEvent.click(await screen.findByRole('button', { name: /Choose a file from your home folder/ }))
    await userEvent.click(await screen.findByRole('button', { name: /reports/ }))
    await userEvent.click(await screen.findByRole('button', { name: /sales.ipynb/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Choose' }))
  }

  it('groups the form into sections named Code, Parameters, Environment variables and Schedule', async () => {
    base()
    renderApp('/jobs/new')
    const code = await screen.findByRole('region', { name: 'Code' })
    expect(within(code).getByRole('button', { name: /Choose a file from your home folder/ })).toBeTruthy()
    expect(within(screen.getByRole('region', { name: 'Parameters' })).getByRole('button', { name: 'Add a parameter' })).toBeTruthy()
    expect(screen.getByRole('region', { name: 'Environment variables' })).toBeTruthy()
    const schedule = screen.getByRole('region', { name: 'Schedule' })
    expect(within(schedule).getByRole('radiogroup', { name: 'When should it run?' })).toBeTruthy()
  })

  it('follows the file: a notebook selects papermill and asks for a kernel', async () => {
    base()
    renderApp('/jobs/new')
    await chooseNotebook()
    expect(await screen.findByText('Notebook (papermill)')).toBeTruthy()
    expect(screen.getByLabelText('Kernel')).toBeTruthy()
  })

  it('explains in Parameters that a notebook gets each value as a variable set after its cell tagged parameters', async () => {
    base()
    renderApp('/jobs/new')
    await chooseNotebook()
    const help = within(screen.getByRole('region', { name: 'Parameters' }))
    expect(help.getByText(/tag one cell parameters/i)).toBeTruthy()
    await userEvent.click(help.getByRole('button', { name: 'Add a parameter' }))
    await userEvent.type(screen.getByLabelText('Parameter 1 name'), 'days')
    await choose(screen.getByLabelText('Parameter 1 type'), 'integer')
    await userEvent.type(screen.getByLabelText('Parameter 1 value'), '7')
    expect(help.getByText('days = 7')).toBeTruthy()
    expect(help.getByText(/^These are default values/).textContent).toMatch(/Run now uses them.*API.*that run only/)
  })

  it('shows a cron the server rejects under the cron field and keeps the form', async () => {
    base({
      'POST /api/schedule/preview': {
        status: 422,
        body: { title: 'Unprocessable', errors: [{ location: 'body.cron', message: 'Hour 25 is out of range.' }] },
      },
    })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: /On a schedule/ }))
    await choose(screen.getByLabelText('Repeats'), 'Custom (cron)')
    const cron = await screen.findByLabelText('Cron')
    await userEvent.clear(cron)
    await userEvent.type(cron, '0 25 * * *')
    await userEvent.tab()
    expect(await screen.findByText('Hour 25 is out of range.')).toBeTruthy()
    expect((screen.getByLabelText('Cron') as HTMLInputElement).value).toBe('0 25 * * *')
    expect(screen.getByRole('heading', { name: 'New job' })).toBeTruthy()
  })

  it('picks the time zone from a list of every zone, and previews in the chosen one', async () => {
    const api = base({ 'POST /api/schedule/preview': { body: { next: [], warnings: [] } } })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: /On a schedule/ }))
    const zone = await screen.findByRole('combobox', { name: 'Time zone' })
    await choose(screen.getByLabelText('Repeats'), 'Custom (cron)')
    await userEvent.clear(screen.getByLabelText('Cron'))
    await userEvent.type(screen.getByLabelText('Cron'), '0 2 * * *')
    // Typing searches every zone by any part of its name, spaces matching underscores.
    await userEvent.clear(zone)
    await userEvent.type(zone, 'new york')
    const zoneOptions = () => {
      const menu = screen.queryByRole('listbox')
      return menu ? within(menu).queryAllByRole('option') : []
    }
    expect(zoneOptions().map((o) => o.textContent)).toEqual(['America/New_York'])
    await userEvent.clear(zone)
    await userEvent.type(zone, 'kolk')
    expect(zoneOptions().map((o) => o.textContent)).toEqual(['Asia/Kolkata'])
    await userEvent.clear(zone)
    await userEvent.type(zone, 'calcutta')
    expect(zoneOptions()).toHaveLength(0)
    await userEvent.clear(zone)
    await userEvent.type(zone, 'tokyo')
    await userEvent.click(screen.getByRole('option', { name: 'Asia/Tokyo' }))
    expect((zone as HTMLInputElement).value).toBe('Asia/Tokyo')
    await waitFor(() =>
      expect(api.calls.some((c) => c.path === '/api/schedule/preview' && (c.body as { timeZone?: string }).timeZone === 'Asia/Tokyo')).toBe(true),
    )
  })

  it('shows the next runs in the zone and in UTC, with one note about daylight saving that can switch to UTC', async () => {
    const api = base({
      'POST /api/schedule/preview': {
        body: {
          next: ['2026-11-02T05:00:00Z', '2026-11-03T05:00:00Z', '2026-11-04T05:00:00Z'],
          warnings: [
            { code: 'DSTSkipped', text: 'Daylight saving on Sun 28 Mar 2027 skips the 02:00 run.', date: '2027-03-28', time: '02:00', until: '02:00', runs: 1 },
            { code: 'DSTRepeated', text: 'Daylight saving on Sun 31 Oct 2027 repeats the 02:00 run.', date: '2027-10-31', time: '02:00', until: '02:00', runs: 1 },
          ],
        },
      },
    })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: /On a schedule/ }))
    await choose(screen.getByLabelText('Repeats'), 'Custom (cron)')
    await userEvent.clear(screen.getByLabelText('Cron'))
    await userEvent.type(screen.getByLabelText('Cron'), '0 2 * * *')
    // A zone with daylight saving, whatever zone the machine running the test is in.
    const zone = screen.getByRole('combobox', { name: 'Time zone' })
    await userEvent.clear(zone)
    await userEvent.type(zone, 'london')
    await userEvent.click(screen.getByRole('option', { name: 'Europe/London' }))
    const note = await screen.findByText(/^Daylight saving: on .*2027 the 02:00 run is skipped, and on .*2027 it runs twice\.$/)
    expect(note.textContent).not.toContain('2027-03-28')
    expect(screen.queryByRole('alert')).toBeNull()
    const list = screen.getByRole('list', { name: 'Next runs' })
    expect(within(list).getAllByRole('listitem')).toHaveLength(3)
    expect(list.textContent).toContain('05:00 UTC')
    await userEvent.click(screen.getByRole('button', { name: 'Use UTC' }))
    expect((screen.getByRole('combobox', { name: 'Time zone' }) as HTMLInputElement).value).toBe('UTC')
    await waitFor(() => expect(api.calls.some((c) => c.path === '/api/schedule/preview' && (c.body as { timeZone?: string }).timeZone === 'UTC')).toBe(true))
  })

  it('starts a new schedule as daily at 09:00 and previews it at once', async () => {
    const api = base({ 'POST /api/schedule/preview': { body: { next: ['2026-11-02T09:00:00Z'], warnings: [] } } })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: /On a schedule/ }))
    expect(screen.getByLabelText('Repeats').textContent).toBe('Daily')
    expect(cronField().value).toBe('0 9 * * *')
    await waitFor(() => expect(api.calls.some((c) => c.path === '/api/schedule/preview' && (c.body as { cron?: string }).cron === '0 9 * * *')).toBe(true))
    expect(await screen.findByRole('list', { name: 'Next runs' })).toBeTruthy()
  })

  it('writes the cron for each kind of schedule', async () => {
    base({ 'POST /api/schedule/preview': { body: { next: [], warnings: [] } } })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: /On a schedule/ }))
    const repeats = screen.getByLabelText('Repeats')
    await choose(repeats, 'Every minute')
    expect(cronField().value).toBe('* * * * *')
    await choose(repeats, 'Every few minutes')
    await choose(screen.getByLabelText('Every'), '15 minutes')
    expect(cronField().value).toBe('*/15 * * * *')
    await choose(repeats, 'Hourly')
    await choose(screen.getByLabelText('At minute'), ':30')
    expect(cronField().value).toBe('30 * * * *')
    await choose(repeats, 'Monthly')
    await choose(screen.getByLabelText('On day'), '15th')
    expect(cronField().value).toBe('30 9 15 * *')
    await choose(repeats, 'Weekly')
    expect(cronField().value).toBe('30 9 * * 1')
    // With no day on, nothing is written and the form says what is missing.
    await userEvent.click(screen.getByRole('button', { name: 'Monday' }))
    expect(screen.getByText('Pick at least one day')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Create job' }) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: 'Monday' }))
    await userEvent.click(screen.getByRole('button', { name: 'Friday' }))
    expect(screen.getByRole('button', { name: 'Friday' }).getAttribute('aria-pressed')).toBe('true')
    expect(cronField().value).toBe('30 9 * * 1,5')
    // Custom starts from what the builder wrote.
    await choose(repeats, 'Custom (cron)')
    expect((screen.getByLabelText('Cron') as HTMLInputElement).value).toBe('30 9 * * 1,5')
  })

  it('opens an existing schedule on its preset', async () => {
    base()
    renderApp('/jobs/weekly-report/edit')
    expect((await screen.findByLabelText('Repeats')).textContent).toBe('Weekly')
    expect(screen.getByRole('button', { name: 'Monday' }).getAttribute('aria-pressed')).toBe('true')
    expect(screen.getByRole('button', { name: 'Tuesday' }).getAttribute('aria-pressed')).toBe('false')
    expect(screen.getByLabelText('Hour').textContent).toMatch(/^06( AM)?$/)
    expect(cronField().value).toBe('0 6 * * 1')
  })

  it('reads a typed cron back into the builder when it matches a preset', async () => {
    base({ 'POST /api/schedule/preview': { body: { next: [], warnings: [] } } })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: /On a schedule/ }))
    await userEvent.clear(cronField())
    await userEvent.type(cronField(), '*/15 * * * *')
    expect(screen.getByLabelText('Repeats').textContent).toBe('Every few minutes')
    expect(screen.getByLabelText('Every').textContent).toBe('15 minutes')
    expect(cronField().value).toBe('*/15 * * * *')
  })

  it('switches to custom and keeps what was typed when no preset matches', async () => {
    base({ 'POST /api/schedule/preview': { body: { next: [], warnings: [] } } })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: /On a schedule/ }))
    await userEvent.clear(cronField())
    await userEvent.type(cronField(), '0 6 1-7 * 1')
    expect(screen.getByLabelText('Repeats').textContent).toBe('Custom (cron)')
    expect(cronField().value).toBe('0 6 1-7 * 1')
  })

  it('waits until the cron field is left before checking a typed expression', async () => {
    const api = base({ 'POST /api/schedule/preview': { body: { next: [], warnings: [] } } })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: /On a schedule/ }))
    // Daily at 09:00 is "0 9 * * *"; typing on turns it into an expression no preset matches.
    await userEvent.type(cronField(), ',1')
    expect(screen.getByLabelText('Repeats').textContent).toBe('Custom (cron)')
    const sent = () => api.calls.filter((c) => c.path === '/api/schedule/preview').map((c) => (c.body as { cron: string }).cron)
    expect(sent()).not.toContain('0 9 * * *,')
    await userEvent.tab()
    await waitFor(() => expect(sent()).toContain('0 9 * * *,1'))
  })

  it('opens a schedule no preset matches as custom cron', async () => {
    base({ 'GET /api/jobs/weekly-report': { body: { ...weeklyDetail, schedule: { ...weeklyDetail.schedule, cron: '0 6 1-7 * 1' } } } })
    renderApp('/jobs/weekly-report/edit')
    expect((await screen.findByLabelText('Repeats')).textContent).toBe('Custom (cron)')
    expect((screen.getByLabelText('Cron') as HTMLInputElement).value).toBe('0 6 1-7 * 1')
  })

  it('creates the job and opens it', async () => {
    const api = base({
      'POST /api/jobs': { status: 201, body: weeklyDetail },
    })
    renderApp('/jobs/new')
    await chooseNotebook()
    await userEvent.clear(await screen.findByLabelText('Name'))
    await userEvent.type(screen.getByLabelText('Name'), 'Weekly report')
    await userEvent.click(screen.getByRole('radio', { name: /On a schedule/ }))
    // Weekly on Monday at 06:00, built without writing cron.
    await choose(screen.getByLabelText('Repeats'), 'Weekly')
    expect(screen.getByRole('button', { name: 'Monday' }).getAttribute('aria-pressed')).toBe('true')
    await choose(screen.getByLabelText('Hour'), /^06( AM)?$/)
    await choose(screen.getByLabelText('Minute'), '00')
    expect(screen.getByText(/^At 06:00( AM)?, only on Monday$/)).toBeTruthy()
    expect(cronField().value).toBe('0 6 * * 1')
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/weekly-report'))
    const body = api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body as Record<string, unknown>
    expect(body).toMatchObject({ displayName: 'Weekly report', engine: 'papermill', source: 'reports/sales.ipynb', template: 'python-jobs', kernel: 'python3' })
    expect(body.schedule).toMatchObject({ cron: '0 6 * * 1' })
  })
})

describe('New command job', () => {
  it('runs a typed command in the home folder and says how parameters arrive', async () => {
    const api = base({
      'POST /api/jobs': { status: 201, body: printTimeDetail },
      'POST /api/jobs/print-the-time/run': { status: 201, body: { ...weeklyDetail.lastRun, id: 'run-1', outcome: 'Running' } },
    })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: 'Command' }))
    const cmd = screen.getByLabelText('Command')
    expect(cmd.className).toContain('mono')
    const hint = document.getElementById(cmd.getAttribute('aria-describedby') ?? '')
    expect(hint?.textContent).toMatch(/home folder.*JAVO_PARAM_<NAME>/)
    await userEvent.type(cmd, 'python etl.py --region emea')
    await userEvent.type(screen.getByLabelText('Name'), 'Print the time')
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/print-the-time'))
    const body = api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body as Record<string, unknown>
    expect(body).toMatchObject({ displayName: 'Print the time', engine: 'command', command: 'python etl.py --region emea', template: 'python-jobs' })
    expect(body.source).toBeUndefined()
  })

  it('explains in Parameters that a command reads each value from its JAVO_PARAM_ variable', async () => {
    base()
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: 'Command' }))
    const help = within(screen.getByRole('region', { name: 'Parameters' }))
    expect(help.getByText('os.environ["JAVO_PARAM_REGION"]')).toBeTruthy()
    await userEvent.click(help.getByRole('button', { name: 'Add a parameter' }))
    await userEvent.type(screen.getByLabelText('Parameter 1 name'), 'start-date')
    expect(help.getByText('os.environ["JAVO_PARAM_START_DATE"]')).toBeTruthy()
  })

  it('explains in Parameters that a bash script reads each value as $JAVO_PARAM_', async () => {
    base({ 'GET /api/templates': { body: { items: [...templates, customJobTemplate, jobTemplate] } } })
    renderApp('/jobs/new')
    await screen.findByLabelText('Command')
    expect(within(screen.getByRole('region', { name: 'Parameters' })).getByText('echo "$JAVO_PARAM_REGION"')).toBeTruthy()
  })

  it('shows what the server says about the command under the field', async () => {
    base({
      'POST /api/jobs': { status: 422, body: { title: 'Unprocessable', errors: [{ location: 'body.command', message: 'Type the command to run.' }] } },
    })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: 'Command' }))
    await userEvent.type(screen.getByLabelText('Command'), 'x')
    await userEvent.type(screen.getByLabelText('Name'), 'A')
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    expect(await screen.findByText('Type the command to run.')).toBeTruthy()
    expect(screen.queryByText('Nothing was saved')).toBeNull()
  })

  it('runs a typed script through bash with the Custom command template', async () => {
    const api = base({
      'GET /api/templates': { body: { items: [...templates, customJobTemplate, jobTemplate] } },
      'POST /api/jobs': { status: 201, body: printTimeDetail },
      'POST /api/jobs/print-the-time/run': { status: 201, body: { ...weeklyDetail.lastRun, id: 'run-1', outcome: 'Running' } },
    })
    renderApp('/jobs/new')
    // First in the picker and chosen to start with; it runs only typed scripts, so there is no File choice.
    const picker = await screen.findByLabelText('Template')
    expect(picker.textContent).toBe('Custom command')
    expect((await optionsOf(picker))[0]).toBe('Custom command')
    const cmd = await screen.findByLabelText('Command')
    expect(screen.queryByRole('radio', { name: 'Command' })).toBeNull()
    expect(cmd.tagName).toBe('TEXTAREA')
    expect(cmd.getAttribute('placeholder')).toContain('echo "Hello from $(hostname)"')
    const hint = document.getElementById(cmd.getAttribute('aria-describedby') ?? '')
    expect(hint?.textContent).toMatch(/bash.*home folder.*\$JAVO_PARAM_<NAME>/)
    await userEvent.type(cmd, 'cd reports{Enter}python etl.py | tee "$JAVO_PARAM_OUT"')
    await userEvent.type(screen.getByLabelText('Name'), 'Print the time')
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/print-the-time'))
    const body = api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body as Record<string, unknown>
    expect(body).toMatchObject({ template: 'jobs-command', engine: 'bash', command: 'cd reports\npython etl.py | tee "$JAVO_PARAM_OUT"' })
  })

  it('edits a command job with its command filled in', async () => {
    base({ 'GET /api/jobs/print-the-time': { body: printTimeDetail } })
    renderApp('/jobs/print-the-time/edit')
    expect(((await screen.findByLabelText('Command')) as HTMLInputElement).value).toBe('bash /opt/javo/samples/jobs/print-time.sh')
    expect(screen.getByRole('radio', { name: 'Command' }).getAttribute('aria-checked')).toBe('true')
  })
})

describe('Run once', () => {
  async function fillRunOnce() {
    await userEvent.click(await screen.findByRole('button', { name: /Choose a file from your home folder/ }))
    await userEvent.click(await screen.findByRole('button', { name: /reports/ }))
    await userEvent.click(await screen.findByRole('button', { name: /sales.ipynb/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Choose' }))
    await userEvent.clear(await screen.findByLabelText('Name'))
    await userEvent.type(screen.getByLabelText('Name'), 'Weekly report')
  }

  it('starts the first run of the saved job by its name, then opens it', async () => {
    const api = base({
      'POST /api/jobs': { status: 201, body: weeklyDetail },
      'POST /api/jobs/weekly-report/run': { status: 201, body: { ...weeklyDetail.lastRun, id: 'run-4', outcome: 'Running' } },
    })
    renderApp('/jobs/new')
    await fillRunOnce()
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/weekly-report'))
    expect(api.count('POST', '/api/jobs/weekly-report/run')).toBe(1)
    expect(api.count('POST', '/api/jobs//run')).toBe(0)
  })

  it('tells the user on the job page when the first run could not start', async () => {
    base({
      'POST /api/jobs': { status: 201, body: weeklyDetail },
      'POST /api/jobs/weekly-report/run': { status: 422, body: { title: 'The notebook has no kernel' } },
    })
    renderApp('/jobs/new')
    await fillRunOnce()
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/weekly-report'))
    expect(await screen.findByText(/The notebook has no kernel/)).toBeTruthy()
  })
})

describe('Home', () => {
  it('lists a failed run under Needs your attention with a link to its job', async () => {
    base()
    renderApp('/')
    const section = (await screen.findByRole('heading', { name: 'Needs your attention' })).closest('section')!
    const link = await within(section).findByRole('link', { name: /Weekly report/ })
    expect(link.getAttribute('href')).toBe('/jobs/weekly-report')
  })
})

describe('New job from a template with a sample job', () => {
  it("starts from the template's sample job, so Create job is all it takes", async () => {
    const api = base({
      'GET /api/templates': { body: { items: [...templates, jobTemplate] } },
      'GET /api/jobs': { body: { items: [{ ...weeklyView, name: 'print-the-time', displayName: 'Print the time' }] } },
      'POST /api/jobs': { status: 201, body: printTimeDetail },
      'POST /api/jobs/print-the-time/run': { status: 201, body: { ...weeklyDetail.lastRun, id: 'run-1', outcome: 'Running' } },
      'GET /api/jobs/print-the-time': { body: printTimeDetail },
    })
    renderApp('/jobs/new')
    expect(((await screen.findByLabelText('Command')) as HTMLInputElement).value).toBe('bash /opt/javo/samples/jobs/print-time.sh')
    await waitFor(() => expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Print the time 2'))
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(api.count('POST', '/api/jobs')).toBe(1))
    expect(api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body).toMatchObject({
      displayName: 'Print the time 2',
      template: 'python-jobs',
      engine: 'command',
      command: 'bash /opt/javo/samples/jobs/print-time.sh',
      parameters: [{ name: 'greeting', type: 'string', default: 'hello' }],
    })
  })

  it('leaves the sample for a file once File is picked', async () => {
    base({ 'GET /api/templates': { body: { items: [...templates, jobTemplate] } } })
    renderApp('/jobs/new')
    await screen.findByLabelText('Command')
    await userEvent.click(screen.getByRole('radio', { name: 'File' }))
    expect(screen.queryByLabelText('Command')).toBeNull()
    expect(screen.queryByLabelText('Parameter 1 name')).toBeNull()
  })
})
