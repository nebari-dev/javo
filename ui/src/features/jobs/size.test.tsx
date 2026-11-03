import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp, type Reply } from '../../test/api'
import { engines, singleNodeSizes, jobTemplate, me, sizes, templates, weeklyDetail, weeklyView } from '../../test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

const home: Reply = { body: { path: '', entries: [{ name: 'reports', kind: 'folder', size: 0, modified: '2026-10-29T10:00:00Z' }] } }
const reports: Reply = { body: { path: 'reports', entries: [{ name: 'sales.ipynb', kind: 'file', size: 4096, modified: '2026-10-30T11:02:00Z' }] } }

function base(extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    // Without its sample job, so a new job starts blank.
    'GET /api/templates': { body: { items: [...templates, { ...jobTemplate, jobs: { ...jobTemplate.jobs!, sample: undefined } }] } },
    'GET /api/engines': { body: { items: engines } },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/sizes': { body: { items: sizes } },
    'GET /api/jobs': { body: { items: [weeklyView] } },
    'GET /api/jobs/weekly-report': { body: weeklyDetail },
    'GET /api/files': (c) => (new URL(c.path, 'http://x').searchParams.get('path') === 'reports' ? reports : home),
    ...extra,
  })
}

describe('New job size', () => {
  async function chooseNotebook() {
    await userEvent.click(await screen.findByRole('button', { name: /Choose a file from your home folder/ }))
    await userEvent.click(await screen.findByRole('button', { name: /reports/ }))
    await userEvent.click(await screen.findByRole('button', { name: /sales.ipynb/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Choose' }))
  }

  it('starts at the default size and sends the size picked', async () => {
    const api = base({ 'POST /api/jobs': { status: 201, body: weeklyDetail } })
    renderApp('/jobs/new')
    const group = await screen.findByRole('radiogroup', { name: 'Size' })
    expect((within(group).getByRole('radio', { name: /Small/ }) as HTMLInputElement).checked).toBe(true)
    await chooseNotebook()
    await userEvent.click(within(group).getByRole('radio', { name: /Medium/ }))
    await userEvent.click(screen.getByRole('radio', { name: /On a schedule/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/weekly-report'))
    expect(api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body).toMatchObject({ size: 'medium' })
  })

  it('offers a size no machine can hold but does not let it be picked', async () => {
    base({ 'GET /api/sizes': { body: { items: singleNodeSizes } } })
    renderApp('/jobs/new')
    const group = await screen.findByRole('radiogroup', { name: 'Size' })
    expect((within(group).getByRole('radio', { name: /Medium/ }) as HTMLInputElement).checked).toBe(true)
    expect((within(group).getByRole('radio', { name: /Large/ }) as HTMLInputElement).disabled).toBe(true)
  })

  it('asks for no size when the admin configured none', async () => {
    base({ 'GET /api/sizes': { body: { items: [] } } })
    renderApp('/jobs/new')
    await screen.findByRole('heading', { name: 'New job' })
    expect(screen.queryByRole('radiogroup', { name: 'Size' })).toBeNull()
  })
})

describe('Job page size', () => {
  it('shows the size and changes it from the next run on', async () => {
    const api = base({ 'PUT /api/jobs/weekly-report/size': { body: { ...weeklyDetail, size: 'medium' } } })
    renderApp('/jobs/weekly-report')
    expect(await screen.findByText('Small · 1 CPU · 2 GB')).toBeTruthy()
    expect(await screen.findByRole('radiogroup', { name: 'Size' })).toBeTruthy()
    expect(screen.getByText('The next run gets the new size; a run already going keeps its size.')).toBeTruthy()
    await userEvent.click(screen.getByRole('radio', { name: /Medium/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Save size' }))
    expect(await screen.findByText('Medium · 4 CPU · 8 GB')).toBeTruthy()
    expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ size: 'medium' })
    expect((screen.getByRole('radio', { name: /Medium/ }) as HTMLInputElement).checked).toBe(true)
  })

  it('shows the GPUs of a GPU size', async () => {
    const training = { name: 'training', displayName: 'Training', cpu: 4, memory: 16 * 2 ** 30, gpu: 1, default: false, fits: true }
    base({
      'GET /api/sizes': { body: { items: [...sizes, training] } },
      'GET /api/jobs/weekly-report': { body: { ...weeklyDetail, size: 'training' } },
    })
    renderApp('/jobs/weekly-report')
    expect(await screen.findByText('Training · 4 CPU · 16 GB · 1 GPU')).toBeTruthy()
  })

  it('offers Change size on a job whose size is gone', async () => {
    const problem = { reason: 'JobSizeUnknown', title: 'Size not available', what: 'w', todo: 't', blame: 'b', who: 'x' }
    base({ 'GET /api/jobs/weekly-report': { body: { ...weeklyDetail, size: 'gone', phase: 'Invalid', problem } } })
    renderApp('/jobs/weekly-report')
    const alert = await screen.findByRole('alert')
    await userEvent.click(within(alert).getByRole('button', { name: 'Change size' }))
    expect(screen.getByRole('radiogroup', { name: 'Size' })).toBeTruthy()
  })
})
