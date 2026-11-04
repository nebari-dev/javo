import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp, type Reply } from '../../test/api'
import { agentTemplate, allowance, containerTemplate, customTemplate, singleNodeSizes, me, nodeTemplate, REASONS, sizes, staticTemplate, stepsAt, storageView, templateCategories, templates, TOO_BIG, ws } from '../../test/fixtures'
import { choose } from '../../test/select'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const home: Reply = {
  body: {
    path: '',
    entries: [
      { name: 'sales', kind: 'folder', size: 0, modified: '2026-10-29T10:00:00Z' },
      { name: 'README.md', kind: 'file', size: 2048, modified: '2026-09-02T10:00:00Z' },
    ],
  },
}
const sales: Reply = {
  body: {
    path: 'sales',
    entries: [
      { name: 'app.py', kind: 'file', size: 4096, modified: '2026-10-30T11:02:00Z' },
      { name: 'sales.csv', kind: 'file', size: 18874368, modified: '2026-10-27T10:00:00Z' },
    ],
  },
}

function files(byPath: Record<string, Reply | Reply[]>) {
  const seen: Record<string, number> = {}
  return (c: { path: string }) => {
    const p = new URL(c.path, 'http://x').searchParams.get('path') ?? ''
    const r = byPath[p]
    if (!Array.isArray(r)) return r ?? { status: 404, body: { title: 'No such folder' } }
    const i = seen[p] ?? 0
    seen[p] = i + 1
    return r[Math.min(i, r.length - 1)]
  }
}

// The same templates without samples: the form asks for the user's own file.
const plain = templates.map(({ sample: _, ...t }) => t)

function base(extra: Record<string, unknown> = {}, items: unknown[] = plain) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items, categories: templateCategories } },
    'GET /api/files': files({ '': home, sales }),
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/sizes': { body: { items: [] } },
    ...extra,
  })
}

async function pickStreamlit() {
  await userEvent.click(await screen.findByRole('radio', { name: /Streamlit/ }))
}

// The code and size cards start as one row each when they hold a default.
const openCode = async () => userEvent.click(await screen.findByRole('button', { name: /^Code: / }))
// The sizes show open, once they have loaded.
const sizesLoaded = async () => screen.findByRole('radiogroup', { name: 'Size' })

describe('New app', () => {
  it('shows the image and command the chosen template runs, filled in as the person types', async () => {
    const custom = { ...customTemplate, sample: undefined, run: { image: 'ghcr.io/example/pixi:1', command: ['/opt/javo/pod-user.sh', '<your command>'], line: '/opt/javo/pod-user.sh <your command>' } }
    base({}, [...plain.filter((t) => t.name !== 'custom'), custom])
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Custom command/ }))
    const runs = screen.getByRole('group', { name: 'Runs' })
    expect(within(runs).getByText('ghcr.io/example/pixi:1')).toBeTruthy()
    const line = () => runs.querySelector('code')?.textContent
    expect(line()).toBe('/opt/javo/pod-user.sh <your command>')
    await userEvent.type(screen.getByLabelText('Command'), 'python app.py')
    expect(line()).toBe('/opt/javo/pod-user.sh python app.py')
  })

  it('keeps keyboard focus inside the file picker and returns it on Escape', async () => {
    base()
    renderApp('/apps/new')
    await pickStreamlit()
    const trigger = screen.getByRole('button', { name: /Choose a file/ })
    await userEvent.click(trigger)
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    expect(picker.getAttribute('aria-modal')).toBe('true')
    await within(picker).findByRole('button', { name: /README\.md/ })
    expect(document.activeElement).toBe(within(picker).getByRole('searchbox', { name: 'Search this folder' }))
    for (let i = 0; i < 12; i++) {
      await userEvent.tab()
      expect(picker.contains(document.activeElement)).toBe(true)
    }
    await userEvent.tab({ shift: true })
    expect(picker.contains(document.activeElement)).toBe(true)
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: /Choose a file/ }))
  })

  it('shows the app, its code and its advanced settings as labelled sections, not as choices side by side', async () => {
    base({}, templates)
    renderApp('/apps/new')
    await pickStreamlit()
    const app = screen.getByRole('region', { name: 'App' })
    expect(within(app).getByRole('button', { name: /Streamlit, change/ })).toBeTruthy()
    const code = screen.getByRole('region', { name: 'Code' })
    expect(within(code).getByRole('button', { name: /^Code: / })).toBeTruthy()
    expect(within(app).queryByRole('button', { name: /^Code: / })).toBeNull()
    expect(screen.getByRole('region', { name: 'Advanced' })).toBeTruthy()
  })

  it('shows the templates as cards', async () => {
    base()
    renderApp('/apps/new')
    const group = await screen.findByRole('radiogroup', { name: 'What do you want to run?' })
    expect(within(group).getByRole('radio', { name: /JupyterLab/ })).toBeTruthy()
    expect(within(group).getByRole('radio', { name: /Streamlit.*Python data apps/ })).toBeTruthy()
  })

  it('groups every template under its category, in the categories order, with no category left empty', async () => {
    const vscode = { ...plain[0], name: 'vscode', displayName: 'VS Code', kind: 'vscode' }
    base({}, [customTemplate, agentTemplate, ...plain, vscode])
    renderApp('/apps/new')
    const group = await screen.findByRole('radiogroup', { name: 'What do you want to run?' })
    const sections = within(group).getAllByRole('group')
    expect(sections.map((s) => s.getAttribute('aria-label') ?? document.getElementById(s.getAttribute('aria-labelledby') ?? '')?.textContent)).toEqual([
      'Notebooks and IDEs',
      'Dashboards and visualization',
      'Web apps and APIs',
      'Coding agents',
    ])
    expect(within(sections[0]).getAllByRole('radio').map((r) => (r as HTMLInputElement).value)).toEqual(['jupyterlab', 'vscode'])
    expect(within(sections[3]).getAllByRole('radio').map((r) => (r as HTMLInputElement).value)).toEqual(['claude-code'])
    expect(within(group).queryByRole('button', { name: /more type/ })).toBeNull()
  })

  it('shows the refusal for a login-less template under the template list and keeps the chosen file', async () => {
    const refusal = 'This cluster does not enforce NetworkPolicy, so this template would be reachable without a login. Pick another template.'
    base({
      'POST /api/workspaces': {
        status: 422,
        body: { status: 422, title: 'Unprocessable Entity', errors: [{ location: 'body.template', message: refusal }] },
      },
    })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    await userEvent.click(await within(picker).findByRole('button', { name: /sales/ }))
    await userEvent.click(await within(picker).findByRole('button', { name: /app\.py/ }))
    await userEvent.click(within(picker).getByRole('button', { name: /^Choose/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    const group = await screen.findByRole('radiogroup', { name: 'What do you want to run?' })
    await waitFor(() => expect(document.getElementById(group.getAttribute('aria-describedby') ?? '')?.textContent).toBe(refusal))
    expect(screen.getByText('sales/app.py')).toBeTruthy()
    expect((screen.getByRole('radio', { name: /Streamlit/ }) as HTMLInputElement).checked).toBe(true)
  })

  it('does not offer job-only templates as apps', async () => {
    const jobsOnly = { ...plain[0], name: 'jobs-py', displayName: 'Python jobs', description: 'Run notebooks in the background.', jobOnly: true, jobs: { engines: ['papermill'], kernels: ['python3'] } }
    base({}, [...plain, jobsOnly])
    renderApp('/apps/new')
    const group = await screen.findByRole('radiogroup', { name: 'What do you want to run?' })
    expect(within(group).getByRole('radio', { name: /JupyterLab/ })).toBeTruthy()
    expect(screen.queryByRole('radio', { name: /Python jobs/ })).toBeNull()
  })

  it('lists home entries in the picker and greys out files the template cannot run', async () => {
    base()
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    expect(await within(picker).findByRole('button', { name: /README\.md/ })).toBeTruthy()
    expect(within(picker).getByRole('button', { name: /README\.md/ }).getAttribute('aria-disabled')).toBe('true')
    await userEvent.click(within(picker).getByRole('button', { name: /sales/ }))
    const app = await within(picker).findByRole('button', { name: /app\.py/ })
    expect(app.getAttribute('aria-disabled')).toBeNull()
    expect(within(picker).getByRole('button', { name: /sales\.csv/ }).getAttribute('aria-disabled')).toBe('true')
    await userEvent.click(app)
    await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(screen.getByText('sales/app.py')).toBeTruthy()
  })

  it('says quietly when the folder listing was cut short', async () => {
    base({ 'GET /api/files': { body: { ...(home.body as object), count: 2, truncated: true } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    expect(await within(picker).findByText('Showing the first 2 items')).toBeTruthy()
  })

  it('does not mention a limit when the listing is complete', async () => {
    base({ 'GET /api/files': { body: { ...(home.body as object), count: 2, truncated: false } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    await within(picker).findByRole('button', { name: /README\.md/ })
    expect(within(picker).queryByText(/Showing the first/)).toBeNull()
  })

  it('waits while the home folder opens, showing placeholder rows, then lists it', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    base({
      'GET /api/files': files({
        '': [{ status: 503, headers: { 'Retry-After': '2' }, body: { status: 503, reason: 'HomeStarting', ...REASONS.HomeStarting } }, home],
      }),
    })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    expect(within(picker).getByRole('status', { name: 'Loading this folder' })).toBeTruthy()
    // A quick open is not explained (see HomePicker.test.tsx for a slow one).
    expect(within(picker).queryByText(REASONS.HomeStarting.title)).toBeNull()
    await act(() => vi.advanceTimersByTimeAsync(500))
    expect(await within(picker).findByRole('button', { name: /README\.md/ })).toBeTruthy()
  })

  it('explains a busy home folder with the failure card from the API', async () => {
    base({ 'GET /api/files': { status: 409, body: { status: 409, reason: 'HomeBusy', ...REASONS.HomeBusy } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    const card = await within(picker).findByRole('alert')
    expect(within(card).getByText('Home folder is busy')).toBeTruthy()
    expect(within(card).getByText(REASONS.HomeBusy.what)).toBeTruthy()
    expect(within(card).getByText(REASONS.HomeBusy.todo)).toBeTruthy()
  })

  it('cannot create a file app before a file is chosen', async () => {
    base()
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.clear(screen.getByLabelText('Name'))
    await userEvent.type(screen.getByLabelText('Name'), 'Sales app')
    expect(screen.getByRole('button', { name: 'Create app' }).hasAttribute('disabled')).toBe(true)
  })

  it('shows a 422 under the named field and keeps the form', async () => {
    base({
      'POST /api/workspaces': {
        status: 422,
        body: { status: 422, title: 'Unprocessable Entity', errors: [{ location: 'body.displayName', message: 'You already have an app called "Sales app".' }] },
      },
    })
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /JupyterLab/ }))
    await userEvent.clear(screen.getByLabelText('Name'))
    await userEvent.type(screen.getByLabelText('Name'), 'Sales app')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    const name = screen.getByLabelText('Name') as HTMLInputElement
    await waitFor(() => expect(name.getAttribute('aria-invalid')).toBe('true'))
    const msg = document.getElementById(name.getAttribute('aria-describedby') ?? '')
    expect(msg?.textContent).toBe('You already have an app called "Sales app".')
    expect(name.value).toBe('Sales app')
    expect(screen.getByRole('button', { name: 'JupyterLab, change' })).toBeTruthy()
    expect(location()).toBe('/apps/new')
  })

  it('shows a 422 for a field the form has no input for at the top of the form', async () => {
    base({
      'POST /api/workspaces': {
        status: 422,
        body: { status: 422, title: 'Unprocessable Entity', errors: [{ location: 'body', message: 'unexpected property' }] },
      },
    })
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /JupyterLab/ }))
    await userEvent.clear(screen.getByLabelText('Name'))
    await userEvent.type(screen.getByLabelText('Name'), 'Lab')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    expect((await screen.findByRole('alert')).textContent).toContain('unexpected property')
  })

  it('creates the app and opens its status page', async () => {
    const created = ws('sales-app', 'Sales app', 'Pending', stepsAt(0, 'Starting'))
    const api = base({ 'POST /api/workspaces': { status: 201, body: created }, 'GET /api/workspaces/sales-app': { body: created } })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.clear(screen.getByLabelText('Name'))
    await userEvent.type(screen.getByLabelText('Name'), 'Sales app')
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    await userEvent.click(await within(picker).findByRole('button', { name: /sales/ }))
    await userEvent.click(await within(picker).findByRole('button', { name: /app\.py/ }))
    await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/sales-app'))
    expect(api.calls.find((c) => c.method === 'POST')?.body).toEqual({ displayName: 'Sales app', template: 'streamlit', file: 'sales/app.py' })
  })

  describe('default name', () => {
    const nameInput = () => screen.getByLabelText('Name') as HTMLInputElement
    const mine = (...names: string[]) => ({
      'GET /api/workspaces': { body: { items: names.map((n, i) => ws(`app-${i}`, n, 'Running', stepsAt(0, 'Starting'))) } },
    })

    it('fills the name with "<template> app" when a template is picked', async () => {
      base()
      renderApp('/apps/new')
      await pickStreamlit()
      expect(nameInput().value).toBe('Streamlit app')
    })

    it('uses the next free number when the user already has that name', async () => {
      base(mine('Streamlit app', 'Streamlit app 2'))
      renderApp('/apps/new')
      await pickStreamlit()
      await waitFor(() => expect(nameInput().value).toBe('Streamlit app 3'))
    })

    it('follows the template while the name is untouched', async () => {
      base()
      renderApp('/apps/new')
      await pickStreamlit()
      await userEvent.click(screen.getByRole('button', { name: 'Streamlit, change' }))
      await userEvent.click(screen.getByRole('radio', { name: /JupyterLab/ }))
      expect(nameInput().value).toBe('JupyterLab app')
    })

    it('never overwrites a typed name', async () => {
      base()
      renderApp('/apps/new')
      await pickStreamlit()
      await userEvent.clear(nameInput())
      await userEvent.type(nameInput(), 'Sales')
      await userEvent.click(screen.getByRole('button', { name: 'Streamlit, change' }))
      await userEvent.click(screen.getByRole('radio', { name: /JupyterLab/ }))
      expect(nameInput().value).toBe('Sales')
    })
  })
})

describe('New app samples', () => {
  const created = ws('streamlit-app', 'Streamlit app', 'Pending', stepsAt(0, 'Starting'))
  const withSamples = (extra: Record<string, unknown> = {}) =>
    base({ 'POST /api/workspaces': { status: 201, body: created }, 'GET /api/workspaces/streamlit-app': { body: created }, ...extra }, templates)
  const createBody = (api: ReturnType<typeof base>) => api.calls.find((c) => c.method === 'POST')?.body

  it('preselects the sample when the picked template has one', async () => {
    withSamples()
    renderApp('/apps/new')
    await pickStreamlit()
    await openCode()
    expect(screen.getByText('Sample: samples/streamlit/app.py (Sales explorer)')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Choose a file/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Create app' }).hasAttribute('disabled')).toBe(false)
  })

  it('creates the sample with the default name in two clicks', async () => {
    const api = withSamples()
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(createBody(api)).toEqual({ displayName: 'Streamlit app', template: 'streamlit', sample: true })
  })

  it('Change opens the file picker and a picked file replaces the sample', async () => {
    const api = withSamples()
    renderApp('/apps/new')
    await pickStreamlit()
    await openCode()
    await userEvent.click(screen.getByRole('button', { name: 'Change' }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    await userEvent.click(within(within(picker).getByRole('navigation', { name: 'Breadcrumbs' })).getByRole('button', { name: 'Home' }))
    await userEvent.click(await within(picker).findByRole('button', { name: /sales/ }))
    await userEvent.click(await within(picker).findByRole('button', { name: /app\.py/ }))
    await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
    expect(screen.getByText('sales/app.py')).toBeTruthy()
    expect(screen.queryByText(/^Sample:/)).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(createBody(api)).toEqual({ displayName: 'Streamlit app', template: 'streamlit', file: 'sales/app.py' })
  })

  it('"Use the sample again" brings the sample back', async () => {
    withSamples()
    renderApp('/apps/new')
    await pickStreamlit()
    await openCode()
    await userEvent.click(screen.getByRole('button', { name: 'Change' }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    await userEvent.click(within(within(picker).getByRole('navigation', { name: 'Breadcrumbs' })).getByRole('button', { name: 'Home' }))
    await userEvent.click(await within(picker).findByRole('button', { name: /sales/ }))
    await userEvent.click(await within(picker).findByRole('button', { name: /app\.py/ }))
    await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
    await userEvent.click(screen.getByRole('button', { name: 'Use the sample again' }))
    expect(screen.getByText('Sample: samples/streamlit/app.py (Sales explorer)')).toBeTruthy()
    expect(screen.queryByText('sales/app.py')).toBeNull()
  })

  it('keeps the sample when the picker is cancelled', async () => {
    withSamples()
    renderApp('/apps/new')
    await pickStreamlit()
    await openCode()
    await userEvent.click(screen.getByRole('button', { name: 'Change' }))
    await screen.findByRole('dialog', { name: 'Choose a file' })
    await userEvent.keyboard('{Escape}')
    expect(screen.getByText('Sample: samples/streamlit/app.py (Sales explorer)')).toBeTruthy()
  })

  // The sample is copied to the home when an app first starts, so it may be there already or not yet.
  const copied: Record<string, Reply> = {
    samples: {
      body: {
        path: 'samples',
        entries: [
          { name: 'jupyterlab', kind: 'folder', size: 0, modified: '2026-10-29T10:00:00Z' },
          { name: 'streamlit', kind: 'folder', size: 0, modified: '2026-10-29T10:00:00Z' },
        ],
      },
    },
    'samples/streamlit': {
      body: {
        path: 'samples/streamlit',
        entries: [
          { name: 'app.py', kind: 'file', size: 4096, modified: '2026-10-29T10:00:00Z' },
          { name: 'sales.csv', kind: 'file', size: 9000, modified: '2026-10-29T10:00:00Z' },
        ],
      },
    },
  }
  const withCopied = () => withSamples({ 'GET /api/files': files({ '': home, sales, ...copied }) })
  const crumbs = (picker: HTMLElement) =>
    within(within(picker).getByRole('navigation', { name: 'Breadcrumbs' }))
      .getAllByRole('button')
      .map((b) => b.textContent)
  const goHome = (picker: HTMLElement) =>
    userEvent.click(within(within(picker).getByRole('navigation', { name: 'Breadcrumbs' })).getByRole('button', { name: 'Home' }))
  const pickJupyter = async () => userEvent.click(await screen.findByRole('radio', { name: /JupyterLab/ }))

  for (const [where, setup, tag] of [
    ['not copied yet', withSamples, 'Sample, copied here when the app starts'],
    ['already copied', withCopied, 'Sample'],
  ] as const) {
    it(`Change opens the picker at the sample file with it selected (${where})`, async () => {
      const api = setup()
      renderApp('/apps/new')
      await pickStreamlit()
      await openCode()
      await userEvent.click(screen.getByRole('button', { name: 'Change' }))
      const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
      const app = await within(picker).findByRole('button', { name: /app\.py/ })
      expect(app.getAttribute('aria-pressed')).toBe('true')
      expect(within(app).getByText(tag)).toBeTruthy()
      expect(crumbs(picker)).toEqual(['Home', 'samples', 'streamlit'])
      expect(within(picker).getByText('samples/streamlit/app.py')).toBeTruthy()
      await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
      expect(screen.queryByRole('dialog')).toBeNull()
      expect(screen.getByText('Sample: samples/streamlit/app.py (Sales explorer)')).toBeTruthy()
      await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
      await waitFor(() => expect(createBody(api)).toEqual({ displayName: 'Streamlit app', template: 'streamlit', sample: true }))
    })

    it(`Change opens a folder picker at the sample folder with it selected (${where})`, async () => {
      const api = setup()
      renderApp('/apps/new')
      await pickJupyter()
      await openCode()
      expect(screen.getByText('Sample: samples/jupyterlab (Notebook tour)')).toBeTruthy()
      await userEvent.click(screen.getByRole('button', { name: 'Change' }))
      const picker = await screen.findByRole('dialog', { name: 'Choose a folder' })
      const lab = await within(picker).findByRole('button', { name: /^jupyterlab/ })
      expect(lab.getAttribute('aria-pressed')).toBe('true')
      expect(within(lab).getByText(tag)).toBeTruthy()
      expect(crumbs(picker)).toEqual(['Home', 'samples'])
      await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
      expect(screen.getByText('Sample: samples/jupyterlab (Notebook tour)')).toBeTruthy()
      await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
      await waitFor(() => expect(createBody(api)).toEqual({ displayName: 'JupyterLab app', template: 'jupyterlab', sample: true }))
    })
  }

  it('shows the missing samples folder as where the sample will be', async () => {
    withSamples()
    renderApp('/apps/new')
    await pickJupyter()
    await openCode()
    await userEvent.click(screen.getByRole('button', { name: 'Change' }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a folder' })
    await within(picker).findByRole('button', { name: /^jupyterlab/ })
    await goHome(picker)
    const samples = await within(picker).findByRole('button', { name: /^samples/ })
    expect(within(samples).getByText('Made when the app starts')).toBeTruthy()
    expect(within(picker).getByRole('button', { name: /^sales/ })).toBeTruthy()
  })

  it('a folder template switches to a folder of your own', async () => {
    const api = withSamples()
    renderApp('/apps/new')
    await pickJupyter()
    await openCode()
    await userEvent.click(screen.getByRole('button', { name: 'Change' }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a folder' })
    await within(picker).findByRole('button', { name: /^jupyterlab/ })
    await goHome(picker)
    const files = await within(picker).findByRole('button', { name: /README\.md/ })
    expect(files.getAttribute('aria-disabled')).toBe('true')
    await userEvent.click(within(picker).getByRole('button', { name: /^sales/ }))
    await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
    expect(screen.queryByText(/^Sample:/)).toBeNull()
    expect(screen.getByText('sales')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(createBody(api)).toEqual({ displayName: 'JupyterLab app', template: 'jupyterlab', file: 'sales' }))
  })

  it('a folder template keeps the sample when the picker is cancelled', async () => {
    withSamples()
    renderApp('/apps/new')
    await pickJupyter()
    await openCode()
    await userEvent.click(screen.getByRole('button', { name: 'Change' }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a folder' })
    await userEvent.click(within(picker).getByRole('button', { name: 'Cancel' }))
    expect(screen.getByText('Sample: samples/jupyterlab (Notebook tour)')).toBeTruthy()
  })

  it('remembers per user whether hidden files are shown', async () => {
    withSamples({ 'GET /api/files': files({ '': { body: { path: '', entries: [{ name: '.config', kind: 'folder', size: 0, modified: '2026-10-29T10:00:00Z' }, ...(home.body as { entries: unknown[] }).entries] } }, sales }) })
    renderApp('/apps/new')
    await pickJupyter()
    await openCode()
    const open = async () => {
      await userEvent.click(screen.getByRole('button', { name: 'Change' }))
      const picker = await screen.findByRole('dialog', { name: 'Choose a folder' })
      await goHome(picker)
      await within(picker).findByRole('button', { name: /^sales/ })
      return picker
    }
    let picker = await open()
    expect(within(picker).queryByRole('button', { name: /^\.config/ })).toBeNull()
    await userEvent.click(within(picker).getByRole('switch', { name: 'Show hidden files' }))
    expect(within(picker).getByRole('button', { name: /^\.config/ })).toBeTruthy()
    await userEvent.click(within(picker).getByRole('button', { name: 'Cancel' }))
    picker = await open()
    expect(within(picker).getByRole('button', { name: /^\.config/ })).toBeTruthy()
    expect(localStorage.getItem(`javo.picker.hidden.${me.id}`)).toBe('show')
  })

  it('keeps the empty file picker for a template without a sample', async () => {
    base()
    renderApp('/apps/new')
    await pickStreamlit()
    expect(screen.getByRole('button', { name: /Choose a file/ })).toBeTruthy()
    expect(screen.queryByText(/^Sample:/)).toBeNull()
    expect(screen.queryByRole('button', { name: 'Use the sample again' })).toBeNull()
  })

  it('has one template picker and no separate sample gallery', async () => {
    base({}, templates)
    renderApp('/apps/new')
    expect(await screen.findByRole('radio', { name: /Streamlit/ })).toBeTruthy()
    expect(screen.queryByRole('region', { name: 'Try a sample' })).toBeNull()
  })
})

describe('New app sizes', () => {
  const created = ws('streamlit-app', 'Streamlit app', 'Pending', stepsAt(0, 'Starting'))
  const withSizes = (extra: Record<string, unknown> = {}) =>
    base(
      {
        'GET /api/sizes': { body: { items: sizes } },
        'POST /api/workspaces': { status: 201, body: created },
        'GET /api/workspaces/streamlit-app': { body: created },
        ...extra,
      },
      templates,
    )
  const createBody = (api: ReturnType<typeof base>) => api.calls.find((c) => c.method === 'POST')?.body

  it('preselects the default size, so the sample is still two clicks', async () => {
    const api = withSizes()
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    const small = await screen.findByRole('radio', { name: /Small/ })
    expect((small as HTMLInputElement).checked).toBe(true)
    expect(screen.getByText('4 CPU · 8 GB')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(createBody(api)).toEqual({ displayName: 'Streamlit app', template: 'streamlit', sample: true, size: 'small' })
  })

  it('creates the app at the picked size', async () => {
    const api = withSizes()
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await userEvent.click(await screen.findByRole('radio', { name: /Large/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(createBody(api)).toMatchObject({ size: 'large' })
  })

  it('offers a size no machine can hold disabled, with why, and preselects the default that fits', async () => {
    const api = withSizes({ 'GET /api/sizes': { body: { items: singleNodeSizes } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    const large = (await screen.findByRole('radio', { name: /Large/ })) as HTMLInputElement
    expect(large.disabled).toBe(true)
    expect(document.getElementById(large.getAttribute('aria-describedby')!)?.textContent).toBe(TOO_BIG)
    // The card says it in a few words; the full sentence is said once, under the sizes.
    expect(screen.getByText('Too big')).toBeTruthy()
    expect((screen.getByRole('radio', { name: /Medium/ }) as HTMLInputElement).checked).toBe(true)
    await userEvent.click(large)
    expect(large.checked).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(createBody(api)).toMatchObject({ size: 'medium' })
  })

  it('shows the GPU count on a GPU size and on no other', async () => {
    const training = { name: 'training', displayName: 'Training', cpu: 4, memory: 16 * 2 ** 30, gpu: 2, default: false, fits: true }
    const api = withSizes({ 'GET /api/sizes': { body: { items: [...sizes, training] } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    const card = (await screen.findByRole('radio', { name: /Training/ })).closest('label')!
    expect(within(card).getByText('2 GPUs')).toBeTruthy()
    expect(screen.getAllByText(/GPU/)).toHaveLength(1)
    await userEvent.click(card)
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(createBody(api)).toMatchObject({ size: 'training' })
  })

  it('offers no custom amount to someone without an allowance', async () => {
    withSizes()
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await screen.findByRole('radio', { name: /Small/ })
    expect(screen.queryByRole('radio', { name: /Custom/ })).toBeNull()
  })

  it('creates the app at a custom amount', async () => {
    const api = withSizes({ 'GET /api/sizes': { body: { items: sizes, custom: allowance } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await userEvent.click(await screen.findByRole('radio', { name: /Custom/ }))
    const cpu = screen.getByRole('spinbutton', { name: 'CPU' }) as HTMLInputElement
    const mem = screen.getByRole('spinbutton', { name: 'Memory' }) as HTMLInputElement
    // It starts from the size that was picked; Custom is the one selection.
    expect(cpu.value).toBe('1')
    expect(mem.value).toBe('2')
    const checked = screen.getAllByRole('radio').filter((r) => (r as HTMLInputElement).name === 'size' && (r as HTMLInputElement).checked)
    expect(checked.map((r) => (r as HTMLInputElement).value)).toEqual(['custom'])
    expect(screen.getByText(allowance.limit!)).toBeTruthy()
    await userEvent.clear(cpu)
    await userEvent.type(cpu, '2.5')
    await userEvent.clear(mem)
    await userEvent.type(mem, '6')
    expect(screen.getByText('2.5 CPU · 6 GB')).toBeTruthy()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    const body = createBody(api) as Record<string, unknown>
    expect(body.resources).toEqual({ cpu: 2.5, memory: 6 * 2 ** 30 })
    expect(body.size).toBeUndefined()
  })

  it('steps the custom amount with the minus and plus keys, within the allowance', async () => {
    const api = withSizes({ 'GET /api/sizes': { body: { items: sizes, custom: allowance } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await userEvent.click(await screen.findByRole('radio', { name: /Custom/ }))
    const cpu = screen.getByRole('spinbutton', { name: 'CPU' }) as HTMLInputElement
    const mem = screen.getByRole('spinbutton', { name: 'Memory' }) as HTMLInputElement
    await userEvent.click(screen.getByRole('button', { name: 'More CPU' }))
    await userEvent.click(screen.getByRole('button', { name: 'More CPU' }))
    expect(cpu.value).toBe('1.2')
    await userEvent.click(screen.getByRole('button', { name: 'Less memory' }))
    expect(mem.value).toBe('1.75')
    // Off the step, a key moves to the next step in its direction.
    await userEvent.clear(cpu)
    await userEvent.type(cpu, '0.15')
    await userEvent.click(screen.getByRole('button', { name: 'Less CPU' }))
    expect(cpu.value).toBe('0.1')
    // At the minimum, minus stops.
    expect((screen.getByRole('button', { name: 'Less CPU' }) as HTMLButtonElement).disabled).toBe(true)
    // Arrow keys step too.
    await userEvent.type(cpu, '{ArrowUp}{ArrowUp}{ArrowUp}{ArrowDown}')
    expect(cpu.value).toBe('0.3')
    // At the maximum, plus stops.
    await userEvent.clear(cpu)
    await userEvent.type(cpu, '8')
    expect((screen.getByRole('button', { name: 'More CPU' }) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.type(cpu, '{ArrowUp}')
    expect(cpu.value).toBe('8')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect((createBody(api) as Record<string, unknown>).resources).toEqual({ cpu: 8, memory: 1.75 * 2 ** 30 })
  })

  it('explains an amount over the limit where it is typed and sends nothing', async () => {
    const api = withSizes({ 'GET /api/sizes': { body: { items: sizes, custom: allowance } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await userEvent.click(await screen.findByRole('radio', { name: /Custom/ }))
    const cpu = screen.getByRole('spinbutton', { name: 'CPU' })
    await userEvent.clear(cpu)
    await userEvent.type(cpu, '9')
    expect(cpu.getAttribute('aria-invalid')).toBe('true')
    expect(document.getElementById(cpu.getAttribute('aria-describedby')!.split(' ').pop()!)?.textContent).toBe(allowance.limit)
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    expect(createBody(api)).toBeUndefined()
  })

  it('collapses the custom amount when a size is picked again', async () => {
    const api = withSizes({ 'GET /api/sizes': { body: { items: sizes, custom: allowance } } })
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await userEvent.click(await screen.findByRole('radio', { name: /Custom/ }))
    await userEvent.click(screen.getByRole('radio', { name: /Medium/ }))
    expect(screen.queryByRole('spinbutton', { name: 'CPU' })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(createBody(api)).toMatchObject({ size: 'medium' }))
  })

  it('shows a 422 on the size under the sizes', async () => {
    withSizes({
      'POST /api/workspaces': {
        status: 422,
        body: { status: 422, title: 'Invalid', errors: [{ location: 'body.size', message: 'Pick one of the sizes on the list.' }] },
      },
    })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    const group = await screen.findByRole('radiogroup', { name: 'Size' })
    expect(group.getAttribute('aria-describedby')).toBeTruthy()
    expect(document.getElementById(group.getAttribute('aria-describedby')!)?.textContent).toBe('Pick one of the sizes on the list.')
    expect(screen.queryByText('Nothing was created')).toBeNull()
  })
})

describe('New app commands and images', () => {
  const created = ws('new-app', 'New app', 'Pending', stepsAt(0, 'Starting'))
  const setup = (extra: Record<string, unknown> = {}) =>
    base({ 'POST /api/workspaces': { status: 201, body: created }, 'GET /api/workspaces/new-app': { body: created }, ...extra }, [
      ...templates,
      customTemplate,
      containerTemplate,
    ])
  const createBody = (api: ReturnType<typeof base>) => api.calls.find((c) => c.method === 'POST')?.body
  const command = () => screen.getByLabelText(/^Command/) as HTMLInputElement

  it('prefills the sample command for a template that takes one and sends it', async () => {
    const api = setup()
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Custom command/ }))
    expect(command().value).toBe(customTemplate.sample!.command)
    expect(command().className).toContain('mono')
    // The hint names the placeholders.
    const hint = document.getElementById(command().getAttribute('aria-describedby')!.split(' ')[0])
    expect(hint?.textContent).toContain('{{.Port}}')
    expect(hint?.textContent).toContain('{{.BaseURL}}')
    expect(hint?.textContent).toContain('{{.Home}}')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/new-app'))
    expect(createBody(api)).toEqual({ displayName: 'Custom command app', template: 'custom', sample: true, command: customTemplate.sample!.command })
  })

  it('asks for a command of your own once you leave the sample, with the file optional', async () => {
    const api = setup()
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Custom command/ }))
    await openCode()
    await userEvent.click(screen.getByRole('button', { name: 'Change' }))
    expect(command().value).toBe('')
    expect(screen.getByText('Type the command')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Create app' }).hasAttribute('disabled')).toBe(true)
    expect(screen.getByRole('button', { name: /Choose a file/ }).textContent).toContain('Optional')
    await userEvent.click(command())
    await userEvent.paste('python app.py --port={{.Port}}')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/new-app'))
    expect(createBody(api)).toEqual({ displayName: 'Custom command app', template: 'custom', command: 'python app.py --port={{.Port}}' })
  })

  it('shows a refused command under the command field', async () => {
    const msg = 'Variables such as $PORT and ~ are not filled in.'
    setup({ 'POST /api/workspaces': { status: 422, body: { status: 422, title: 'Invalid', errors: [{ location: 'body.command', message: msg }] } } })
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Custom command/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(command().getAttribute('aria-invalid')).toBe('true'))
    expect(command().getAttribute('aria-describedby')).toContain(screen.getByText(msg).id)
  })

  const imageSample = { ...containerTemplate, sample: { title: 'Toolbox', file: '', command: 'serve --port={{.Port}}', image: 'example.com/toolbox:1', port: 9000 } }
  const setupImageSample = () =>
    base({ 'POST /api/workspaces': { status: 201, body: created }, 'GET /api/workspaces/new-app': { body: created } }, [...templates, imageSample])

  it('starts from the image sample, and "Use the sample" brings it back', async () => {
    const api = setupImageSample()
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Container image/ }))
    const image = screen.getByLabelText('Image') as HTMLInputElement
    expect(image.value).toBe('example.com/toolbox:1')
    expect((screen.getByLabelText('Port') as HTMLInputElement).value).toBe('9000')
    expect(command().value).toBe('serve --port={{.Port}}')
    expect(screen.queryByRole('button', { name: /Use the sample/ })).toBeNull()
    await userEvent.clear(image)
    await userEvent.click(screen.getByRole('button', { name: /Use the sample/ }))
    expect(image.value).toBe('example.com/toolbox:1')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/new-app'))
    expect(createBody(api)).toEqual({
      displayName: 'Container image app',
      template: 'container',
      image: 'example.com/toolbox:1',
      port: 9000,
      command: 'serve --port={{.Port}}',
    })
  })

  it('arrives from a sample card with the image sample filled in', async () => {
    setupImageSample()
    renderApp('/apps/new?template=container&sample=1')
    expect(((await screen.findByLabelText('Image')) as HTMLInputElement).value).toBe('example.com/toolbox:1')
    expect((screen.getByLabelText('Port') as HTMLInputElement).value).toBe('9000')
    expect(screen.getByRole('button', { name: 'Create app' }).hasAttribute('disabled')).toBe(false)
  })

  it('asks for an image, a port and an optional command instead of a file', async () => {
    const api = setup()
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /Container image/ }))
    expect(screen.queryByRole('button', { name: /Choose a file/ })).toBeNull()
    const image = screen.getByLabelText('Image') as HTMLInputElement
    const port = screen.getByLabelText('Port') as HTMLInputElement
    expect(port.value).toBe('8080')
    expect(command().value).toBe('')
    expect(screen.getByText('Name the image')).toBeTruthy()
    await userEvent.type(image, 'nginxinc/nginx-unprivileged:1.27')
    await userEvent.clear(port)
    await userEvent.type(port, '9000')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/new-app'))
    expect(createBody(api)).toEqual({ displayName: 'Container image app', template: 'container', image: 'nginxinc/nginx-unprivileged:1.27', port: 9000 })
  })
})

describe('New app placement', () => {
  const placement = (over: object) => ({
    coLocated: true,
    fits: true,
    freeCPU: 5,
    freeMemory: 8 * 2 ** 30,
    node: 'cpu-standard-3',
    runningApps: ['Sales app', 'ETL notebook'],
    // The banner text is the API's notice; the page writes none of it.
    notice: {
      code: 'PlacementFits',
      tone: 'info',
      title: 'Will run next to your other apps',
      text: 'You have 2 apps on machine cpu-standard-3 with 5 CPUs free. This app fits.',
      action: 'Nothing to do.',
    },
    ...over,
  })
  const waits = {
    code: 'PlacementWaits',
    tone: 'warn',
    title: 'Will run next to your other apps',
    text: 'You have 2 apps on machine cpu-standard-3 with 5 CPUs free. This app will wait until you stop something.',
    action: 'Stop an app to make room.',
  }
  const withPlacement = (body: object) =>
    base({ 'GET /api/storage/placement': { body }, 'GET /api/storage': { body: storageView() } })

  it('warns that an app which does not fit will wait, and still lets the user create it', async () => {
    withPlacement(placement({ fits: false, notice: waits }))
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /JupyterLab/ }))
    const banner = (await screen.findByText('Will run next to your other apps')).closest('.banner') as HTMLElement
    expect(banner.className).toContain('warn')
    expect(within(banner).getByText(/will wait until you stop something/)).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Create app' }) as HTMLButtonElement).disabled).toBe(false)
  })

  it('says the app fits, as information', async () => {
    withPlacement(placement({ fits: true }))
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /JupyterLab/ }))
    const banner = (await screen.findByText('Will run next to your other apps')).closest('.banner') as HTMLElement
    expect(banner.className).toContain('info')
    expect(within(banner).getByText('Nothing to do.')).toBeTruthy()
    expect(within(banner).getByText(/2 apps on machine cpu-standard-3/)).toBeTruthy()
  })

  it('says nothing when the apps are free to run anywhere', async () => {
    const api = withPlacement(placement({ coLocated: false, node: undefined, runningApps: [], notice: undefined }))
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /JupyterLab/ }))
    await waitFor(() => expect(api.count('GET', '/api/storage/placement')).toBe(1))
    expect(screen.queryByText('Will run next to your other apps')).toBeNull()
  })

  it('asks for the placement of the chosen template and size', async () => {
    const api = withPlacement(placement({}))
    renderApp('/apps/new')
    await userEvent.click(await screen.findByRole('radio', { name: /JupyterLab/ }))
    await waitFor(() => expect(api.count('GET', '/api/storage/placement')).toBe(1))
    expect(api.calls.find((c) => c.path.startsWith('/api/storage/placement'))?.path).toContain('template=jupyterlab')
  })

  it('lists each ready group folder in the picker and disables one that is not ready, with its reason', async () => {
    base({
      'GET /api/storage': {
        body: storageView({
          folders: [
            { dir: 'data-science', group: 'data-science', ready: true },
            {
              dir: 'ml',
              group: 'ml',
              ready: false,
              reason: { code: 'SharedFoldersUnavailable', title: 'Group folders are not available', what: 'w', todo: 't' },
            },
          ],
        }),
      },
    })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    expect(await within(picker).findByRole('button', { name: 'data-science' })).toBeTruthy()
    const off = within(picker).getByRole('button', { name: /^ml/ })
    expect(off.getAttribute('aria-disabled')).toBe('true')
    const why = document.getElementById(off.getAttribute('aria-describedby') ?? '')
    expect(why?.textContent).toBe('Group folders are not available')
  })

  it('asks for the group folder when its place is chosen', async () => {
    const api = base({
      'GET /api/storage': { body: storageView({ folders: [{ dir: 'data-science', group: 'data-science', ready: true }] }) },
    })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    await userEvent.click(await within(picker).findByRole('button', { name: 'data-science' }))
    await waitFor(() => expect(api.calls.some((c) => c.path.includes('place=group%3Adata-science'))).toBe(true))
  })
})

describe('New app from a Git repository', () => {
  const created = { body: ws('new-app', 'New app', 'Pending', stepsAt(0, 'Starting')) }
  const post = (api: ReturnType<typeof base>) => api.calls.find((c) => c.method === 'POST' && c.path === '/api/workspaces')?.body
  const all = [...plain, staticTemplate, nodeTemplate]

  async function pick(name: RegExp) {
    await userEvent.click(await screen.findByRole('radio', { name }))
  }

  it('tells a template that runs from a repository to listen on $PORT, and leaves it out for one that cannot', async () => {
    base({}, [...all, containerTemplate])
    renderApp('/apps/new')
    await pick(/Node back-end/)
    expect(screen.getByLabelText('Command').closest('.field')?.textContent).toContain('listen on the port in $PORT')
    await userEvent.click(screen.getByRole('button', { name: 'Node back-end, change' }))
    await pick(/Container image/)
    expect(screen.getByLabelText('Command (optional)').closest('.field')?.textContent).not.toContain('$PORT')
  })

  it('posts source with the address and branch, and no token when none is typed', async () => {
    const api = base({ 'POST /api/workspaces': created }, all)
    renderApp('/apps/new')
    await pick(/Streamlit/)
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    await userEvent.type(screen.getByLabelText('Repository address'), 'https://github.com/example-org/site')
    await userEvent.type(screen.getByLabelText('Branch, tag or commit'), 'main')
    await userEvent.type(screen.getByLabelText('App file'), 'app.py')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/new-app'))
    expect(post(api)).toEqual({
      displayName: 'Streamlit app',
      template: 'streamlit',
      file: 'app.py',
      source: { url: 'https://github.com/example-org/site', ref: 'main' },
    })
  })

  it('sends the token secret, folder and a changed build only when given', async () => {
    const api = base(
      { 'POST /api/workspaces': created, 'GET /api/secrets': { body: [{ name: 'GH_TOKEN', note: '', createdAt: '2026-10-01T09:00:00Z', updatedAt: '2026-10-01T09:00:00Z', apps: [], jobs: [] }] } },
      all,
    )
    renderApp('/apps/new')
    await pick(/Static site/)
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    await userEvent.type(screen.getByLabelText('Repository address'), 'https://github.com/example-org/private-site')
    await userEvent.click(screen.getByRole('button', { name: 'Folder, access token and build options' }))
    await choose(screen.getByLabelText('Access token'), 'GH_TOKEN')
    await userEvent.type(screen.getByLabelText('Folder in the repository'), 'web')
    expect((screen.getByLabelText('Build command') as HTMLInputElement).value).toBe('npm ci && npm run build')
    expect((screen.getByLabelText('Output folder') as HTMLInputElement).value).toBe('dist')
    await userEvent.clear(screen.getByLabelText('Output folder'))
    await userEvent.type(screen.getByLabelText('Output folder'), 'build')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/new-app'))
    expect((post(api) as { source: unknown }).source).toEqual({
      url: 'https://github.com/example-org/private-site',
      path: 'web',
      tokenSecret: 'GH_TOKEN',
      build: { command: 'npm ci && npm run build', output: 'build' },
    })
  })

  it('says in the form how a site is built and served, which its card leaves out', async () => {
    base({}, all)
    renderApp('/apps/new')
    await pick(/Static site/)
    expect(screen.getByText(/copied when the app starts/)).toBeTruthy()
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    expect(screen.getByText(/A folder with a package\.json is built first; one without is served as it is\./)).toBeTruthy()
    expect(screen.getByText(/relative links/)).toBeTruthy()
  })

  it('leaves the build out when it is the template default', async () => {
    const api = base({ 'POST /api/workspaces': created }, all)
    renderApp('/apps/new')
    await pick(/Static site/)
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    await userEvent.type(screen.getByLabelText('Repository address'), 'https://github.com/example-org/site')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/new-app'))
    expect(post(api)).toEqual({ displayName: 'Static site app', template: 'static-site', source: { url: 'https://github.com/example-org/site' } })
  })

  it('offers only the repository for a template that needs one, and asks for the command', async () => {
    base({}, [...all, { ...nodeTemplate, name: 'repo-only', displayName: 'Repo only', fromRepo: 'Required' }])
    renderApp('/apps/new')
    await pick(/Repo only/)
    expect(screen.queryByRole('radio', { name: 'Git repository' })).toBeNull()
    expect(screen.queryByRole('radio', { name: 'Home folder' })).toBeNull()
    expect(screen.queryByRole('button', { name: /Choose a file/ })).toBeNull()
    expect(screen.getByLabelText('Repository address')).toBeTruthy()
    expect(screen.getByText('Enter the repository address')).toBeTruthy()
  })

  it('offers no repository for a template that runs from the home folder', async () => {
    base({}, all)
    renderApp('/apps/new')
    await pick(/JupyterLab/)
    expect(screen.queryByRole('radio', { name: 'Git repository' })).toBeNull()
    expect(screen.queryByLabelText('Repository address')).toBeNull()
  })

  it('offers the code source as two cards that say what each does', async () => {
    base({}, all)
    renderApp('/apps/new')
    await pick(/Static site/)
    const group = screen.getByRole('radiogroup', { name: 'Where is the code?' })
    expect(within(group).getByText('Files you keep here')).toBeTruthy()
    expect(within(group).getByText('Clone from GitHub')).toBeTruthy()
    await userEvent.click(within(group).getByRole('radio', { name: 'Git repository' }))
    expect((within(group).getByRole('radio', { name: 'Git repository' }) as HTMLInputElement).checked).toBe(true)
  })

  it('lets a static site pick only an HTML file from the home folder', async () => {
    const entries = [
      { name: 'index.html', kind: 'file', size: 512, modified: '2026-10-30T11:02:00Z' },
      { name: 'README.md', kind: 'file', size: 2048, modified: '2026-09-02T10:00:00Z' },
    ]
    base({ 'GET /api/files': { body: { path: '', entries } } }, all)
    renderApp('/apps/new')
    await pick(/Static site/)
    expect((screen.getByRole('radio', { name: 'Home folder' }) as HTMLInputElement).checked).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: /Choose a file/ }))
    const picker = await screen.findByRole('dialog', { name: 'Choose a file' })
    expect((await within(picker).findByRole('button', { name: /index\.html/ })).getAttribute('aria-disabled')).not.toBe('true')
    expect(within(picker).getByRole('button', { name: /README\.md/ }).getAttribute('aria-disabled')).toBe('true')
  })

  it('shows the API text under the address when it is refused', async () => {
    base(
      {
        'POST /api/workspaces': {
          status: 422,
          body: { title: 'Unprocessable Entity', errors: [{ location: 'body.source.url', message: 'Only https addresses work. Use an address that starts with https://.' }] },
        },
      },
      all,
    )
    renderApp('/apps/new')
    await pick(/Node back-end/)
    await openCode()
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    await userEvent.type(screen.getByLabelText('Repository address'), 'http://example.org/site.git')
    await userEvent.type(screen.getByLabelText('Command'), 'node server.js')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    const msg = await screen.findByText(/Only https addresses work/)
    expect(msg.closest('.field')?.querySelector('input')?.id).toBe(screen.getByLabelText('Repository address').id)
    expect(screen.queryByText('Nothing was created')).toBeNull()
  })

  it('opens the hidden options when the API refuses a field inside them', async () => {
    base(
      {
        'POST /api/workspaces': {
          status: 422,
          body: { title: 'Unprocessable Entity', errors: [{ location: 'body.source.path', message: 'Use a folder inside the repository, such as web or docs/site.' }] },
        },
      },
      all,
    )
    renderApp('/apps/new')
    await pick(/Node back-end/)
    await openCode()
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    await userEvent.type(screen.getByLabelText('Repository address'), 'https://github.com/example-org/site')
    await userEvent.type(screen.getByLabelText('Command'), 'node server.js')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    expect(await screen.findByText(/Use a folder inside the repository/)).toBeTruthy()
    expect(screen.getByLabelText('Folder in the repository')).toBeTruthy()
  })

  it('does not carry a home file over to the repository choice', async () => {
    base({}, all)
    renderApp('/apps/new')
    await pick(/Streamlit/)
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    expect(screen.queryByRole('button', { name: /Choose a file/ })).toBeNull()
    expect(screen.getByText('Enter the repository address')).toBeTruthy()
  })

  const options = { body: { available: true, max: 3, maxAllowed: 10, forksWarning: 'A pull request from a fork runs code you have not reviewed.' } }
  const hook = { id: 'h'.repeat(22), url: 'https://javo.example.org/hooks/github/' + 'h'.repeat(22), secret: 's'.repeat(43), notice: 'Copy the secret now.' }

  it('sets up previews and the webhook before the app exists, and sends both with it', async () => {
    const api = base({ 'POST /api/workspaces': created, 'GET /api/previews': options, 'POST /api/previews/webhook': { body: hook } }, all)
    renderApp('/apps/new')
    await pick(/Streamlit/)
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    await userEvent.type(screen.getByLabelText('Repository address'), 'https://github.com/example-org/site')
    await userEvent.type(screen.getByLabelText('App file'), 'app.py')
    await userEvent.click(await screen.findByRole('switch', { name: 'Preview open pull requests' }))
    await userEvent.click(screen.getByRole('button', { name: 'Limit, pull requests from forks and webhook' }))
    await choose(screen.getByLabelText('Previews at most'), '2')
    await userEvent.click(screen.getByRole('button', { name: 'Set up webhook' }))
    expect(await screen.findByText(hook.url)).toBeTruthy()
    expect(screen.getByText(hook.secret)).toBeTruthy()
    expect(screen.getByText(hook.notice)).toBeTruthy()
    expect(api.calls.find((c) => c.path === '/api/previews/webhook')?.body).toEqual({ url: 'https://github.com/example-org/site' })
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/new-app'))
    expect((post(api) as { source: unknown }).source).toEqual({
      url: 'https://github.com/example-org/site',
      previews: { enabled: true, max: 2, forks: false },
      webhook: { id: hook.id, secret: hook.secret },
    })
  })

  it('offers previews only for a GitHub repository on a platform that has them', async () => {
    base({ 'GET /api/previews': options }, all)
    renderApp('/apps/new')
    await pick(/Streamlit/)
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    await userEvent.type(screen.getByLabelText('Repository address'), 'https://git.example.org/example-org/site')
    expect(screen.queryByRole('switch', { name: 'Preview open pull requests' })).toBeNull()
    await userEvent.clear(screen.getByLabelText('Repository address'))
    await userEvent.type(screen.getByLabelText('Repository address'), 'https://github.com/example-org/site')
    expect(await screen.findByRole('switch', { name: 'Preview open pull requests' })).toBeTruthy()
  })

  it('offers no previews when the platform has none', async () => {
    base({ 'GET /api/previews': { body: { ...options.body, available: false } } }, all)
    renderApp('/apps/new')
    await pick(/Streamlit/)
    await userEvent.click(screen.getByRole('radio', { name: 'Git repository' }))
    await userEvent.type(screen.getByLabelText('Repository address'), 'https://github.com/example-org/site')
    await waitFor(() => expect(screen.queryByText('Previews')).toBeNull())
    expect(screen.queryByRole('switch', { name: 'Preview open pull requests' })).toBeNull()
  })
})

describe('New app template picker', () => {
  const grid = () => screen.queryByRole('radiogroup', { name: 'What do you want to run?' })

  it('collapses to the picked template once one is picked, with focus on it', async () => {
    base()
    renderApp('/apps/new')
    await pickStreamlit()
    expect(grid()).toBeNull()
    expect(screen.queryByRole('radio', { name: /JupyterLab/ })).toBeNull()
    const chosen = screen.getByRole('button', { name: 'Streamlit, change' })
    expect(chosen.textContent).toContain('Python data apps')
    expect(document.activeElement).toBe(chosen)
  })

  it('Change reopens every template with the current one selected and focused, and a new pick collapses again', async () => {
    base()
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: 'Streamlit, change' }))
    const streamlit = within(grid()!).getByRole('radio', { name: /Streamlit/ }) as HTMLInputElement
    expect(streamlit.checked).toBe(true)
    expect(document.activeElement).toBe(streamlit)
    await userEvent.click(within(grid()!).getByRole('radio', { name: /JupyterLab/ }))
    expect(grid()).toBeNull()
    expect(screen.getByRole('button', { name: 'JupyterLab, change' })).toBeTruthy()
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('JupyterLab app')
  })

  it('moves through the templates with the arrow keys without collapsing, and Enter keeps the one reached', async () => {
    base()
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: 'Streamlit, change' }))
    const before = document.activeElement
    await userEvent.keyboard('{ArrowLeft}')
    expect(grid()).not.toBeNull()
    expect(document.activeElement).not.toBe(before)
    const reached = document.activeElement as HTMLInputElement
    await userEvent.keyboard('{Enter}')
    expect(grid()).toBeNull()
    expect(location()).toBe('/apps/new')
    expect(screen.getByRole('button', { name: new RegExp(`, change$`) }).textContent).toContain(
      templates.find((t) => t.name === reached.value)!.displayName,
    )
  })

  it('starts collapsed when it arrives with a template chosen, and Create still works', async () => {
    const created = ws('jupyterlab-app', 'JupyterLab app', 'Pending', stepsAt(0, 'Starting'))
    const api = base({ 'POST /api/workspaces': { status: 201, body: created }, 'GET /api/workspaces/jupyterlab-app': { body: created } }, templates)
    renderApp('/apps/new?template=jupyterlab')
    expect(await screen.findByRole('button', { name: 'JupyterLab, change' })).toBeTruthy()
    expect(grid()).toBeNull()
    expect(screen.getByRole('button', { name: 'Show advanced settings' }).getAttribute('aria-expanded')).toBe('false')
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/jupyterlab-app'))
    expect(api.calls.find((c) => c.method === 'POST')?.body).toEqual({ displayName: 'JupyterLab app', template: 'jupyterlab', sample: true })
  })

  it('shows every template before one is chosen', async () => {
    base()
    renderApp('/apps/new')
    expect(await screen.findByRole('radiogroup', { name: 'What do you want to run?' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /, change$/ })).toBeNull()
  })
})

describe('New app folded cards', () => {
  const created = ws('streamlit-app', 'Streamlit app', 'Pending', stepsAt(0, 'Starting'))
  const setup = (extra: Record<string, unknown> = {}, items: unknown[] = templates) =>
    base(
      {
        'GET /api/sizes': { body: { items: sizes } },
        'POST /api/workspaces': { status: 201, body: created },
        'GET /api/workspaces/streamlit-app': { body: created },
        ...extra,
      },
      items,
    )
  const codeRow = () => screen.queryByRole('button', { name: /^Code: .*, change$/ })
  const sizeRow = () => screen.queryByRole('button', { name: /^Size: .*, change$/ })
  const codeGroup = () => screen.queryByRole('radiogroup', { name: 'Where is the code?' })
  const sizeGroup = () => screen.queryByRole('radiogroup', { name: 'Size' })

  it('shows the code as one row when it holds a default, and the sizes open', async () => {
    setup()
    renderApp('/apps/new')
    await pickStreamlit()
    expect(codeRow()?.textContent).toContain('Home folder')
    expect(codeRow()?.textContent).toContain('Sales explorer')
    expect(codeGroup()).toBeNull()
    expect(screen.queryByText(/^Sample:/)).toBeNull()
    const group = await screen.findByRole('radiogroup', { name: 'Size' })
    expect((within(group).getByRole('radio', { name: /Small/ }) as HTMLInputElement).checked).toBe(true)
    expect(sizeRow()).toBeNull()
  })

  it('Change opens the code card with the current choice focused', async () => {
    setup()
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(codeRow()!)
    expect(codeRow()).toBeNull()
    const homeFolder = within(codeGroup()!).getByRole('radio', { name: 'Home folder' }) as HTMLInputElement
    expect(homeFolder.checked).toBe(true)
    expect(document.activeElement).toBe(homeFolder)
    expect(screen.getByText(/^Sample: samples\/streamlit\/app\.py/)).toBeTruthy()
  })

  it('opens the code card from the keyboard', async () => {
    setup()
    renderApp('/apps/new')
    await pickStreamlit()
    codeRow()!.focus()
    await userEvent.keyboard('{Enter}')
    expect(codeGroup()).not.toBeNull()
  })

  it('sends a size picked from the open sizes', async () => {
    const api = setup()
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await userEvent.click(screen.getByRole('radio', { name: /Large/ }))
    expect(sizeGroup()).not.toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(api.calls.find((c) => c.method === 'POST')?.body).toMatchObject({ size: 'large' })
  })

  it('keeps the code card open while it still needs a file', async () => {
    setup({}, plain)
    renderApp('/apps/new')
    await pickStreamlit()
    expect(codeRow()).toBeNull()
    expect(codeGroup()).not.toBeNull()
    expect(screen.getByRole('button', { name: /Choose a file/ })).toBeTruthy()
  })

  it('keeps the size card open when no size is the default', async () => {
    setup({ 'GET /api/sizes': { body: { items: sizes.map((s) => ({ ...s, default: false })) } } })
    renderApp('/apps/new')
    await pickStreamlit()
    expect(await screen.findByRole('radiogroup', { name: 'Size' })).toBeTruthy()
    expect(sizeRow()).toBeNull()
  })

  it('opens the card whose field the API refused', async () => {
    setup({
      'POST /api/workspaces': {
        status: 422,
        body: {
          status: 422,
          title: 'Invalid',
          errors: [
            { location: 'body.file', message: 'The sample is gone.' },
            { location: 'body.size', message: 'Pick one of the sizes on the list.' },
          ],
        },
      },
    })
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    expect(await screen.findByText('The sample is gone.')).toBeTruthy()
    expect(codeRow()).toBeNull()
    expect(codeGroup()).not.toBeNull()
    expect(sizeRow()).toBeNull()
    expect(document.getElementById(sizeGroup()!.getAttribute('aria-describedby')!)?.textContent).toBe('Pick one of the sizes on the list.')
  })

  it('creates from the folded cards with the same body as before', async () => {
    const api = setup()
    renderApp('/apps/new')
    await pickStreamlit()
    await sizesLoaded()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(api.calls.find((c) => c.method === 'POST')?.body).toEqual({ displayName: 'Streamlit app', template: 'streamlit', sample: true, size: 'small' })
  })

  it('folds the code card again when another template is picked', async () => {
    setup()
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(codeRow()!)
    await userEvent.click(screen.getByRole('button', { name: 'Streamlit, change' }))
    await userEvent.click(screen.getByRole('radio', { name: /JupyterLab/ }))
    expect(codeRow()?.textContent).toContain('Notebook tour')
  })
})

describe('New app template default size', () => {
  const created = ws('streamlit-app', 'Streamlit app', 'Pending', stepsAt(0, 'Starting'))
  const MiB = 2 ** 20
  const setup = (defaultResources: { cpu: number; memory: number }, custom?: typeof allowance) =>
    base(
      {
        'GET /api/sizes': { body: { items: sizes, ...(custom ? { custom } : {}) } },
        'POST /api/workspaces': { status: 201, body: created },
        'GET /api/workspaces/streamlit-app': { body: created },
      },
      templates.map((t) => (t.name === 'streamlit' ? { ...t, defaultResources } : t)),
    )
  const createBody = (api: ReturnType<typeof base>) => api.calls.find((c) => c.method === 'POST')?.body as Record<string, unknown>

  it("starts at the template's default amount for someone who may ask for one", async () => {
    const api = setup({ cpu: 0.25, memory: 512 * MiB }, allowance)
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(createBody(api).resources).toEqual({ cpu: 0.25, memory: 512 * MiB })
    expect(createBody(api).size).toBeUndefined()
  })

  it('starts at the smallest size that holds the default for everyone else', async () => {
    const api = setup({ cpu: 2, memory: 4 * 2 ** 30 })
    renderApp('/apps/new')
    await pickStreamlit()
    await userEvent.click(screen.getByRole('button', { name: 'Create app' }))
    await waitFor(() => expect(location()).toBe('/apps/streamlit-app'))
    expect(createBody(api).size).toBe('medium')
  })
})
