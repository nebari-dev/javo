import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { SecretView } from '../../api'
import { location, mockApi, renderApp } from '../../test/api'
import { engines, jobTemplate, me, printTimeDetail, templates, weeklyDetail } from '../../test/fixtures'
import { choose } from '../../test/select'

afterEach(() => vi.unstubAllGlobals())

const warehouse: SecretView = {
  name: 'WAREHOUSE_PASSWORD',
  note: '',
  createdAt: '2026-10-02T09:00:00Z',
  updatedAt: '2026-10-02T09:00:00Z',
  apps: [],
  jobs: [{ name: 'weekly-report', displayName: 'Weekly report' }],
}
const openai: SecretView = { ...warehouse, name: 'OPENAI_KEY', apps: [{ name: 'dash', displayName: 'Sales dashboard' }], jobs: [] }

function base(extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/templates': { body: { items: [...templates, jobTemplate] } },
    'GET /api/engines': { body: { items: engines } },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/jobs': { body: { items: [] } },
    'GET /api/secrets': { body: [openai, warehouse] },
    ...extra,
  })
}

describe('Environment variables of jobs', () => {
  it('sends the typed values and secrets set on New job', async () => {
    const api = base({
      'POST /api/jobs': { status: 201, body: printTimeDetail },
      'POST /api/jobs/print-the-time/run': { status: 201, body: { ...weeklyDetail.runs[0], id: 'run-1', outcome: 'Running' } },
      'GET /api/jobs/print-the-time': { body: printTimeDetail },
    })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: 'Command' }))
    await userEvent.type(screen.getByLabelText('Command'), 'python etl.py')
    await userEvent.type(screen.getByLabelText('Name'), 'Print the time')
    const card = screen.getByRole('heading', { name: 'Environment variables' }).closest('.part') as HTMLElement
    expect(within(card).getByText(/Each run gets each as an environment variable/)).toBeTruthy()
    await userEvent.click(within(card).getByRole('button', { name: 'Add a variable' }))
    await userEvent.type(within(card).getByLabelText('Name'), 'REGION')
    await userEvent.type(within(card).getByLabelText('Value'), 'emea')
    await userEvent.click(within(card).getByRole('button', { name: 'Add a variable' }))
    await choose(within(card).getAllByLabelText('From')[1], 'Secret')
    await userEvent.click(within(card).getByRole('combobox', { name: 'Secret' }))
    await userEvent.click(await screen.findByRole('option', { name: 'WAREHOUSE_PASSWORD' }))
    const name = within(card).getAllByLabelText('Name')[1]
    await userEvent.clear(name)
    await userEvent.type(name, 'PGPASSWORD')
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/print-the-time'))
    expect(api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body).toMatchObject({
      env: [
        { name: 'REGION', value: 'emea' },
        { name: 'PGPASSWORD', secret: 'WAREHOUSE_PASSWORD' },
      ],
    })
  })

  it('sends no variables when none are set', async () => {
    const api = base({
      'POST /api/jobs': { status: 201, body: printTimeDetail },
      'POST /api/jobs/print-the-time/run': { status: 201, body: { ...weeklyDetail.runs[0], id: 'run-1', outcome: 'Running' } },
      'GET /api/jobs/print-the-time': { body: printTimeDetail },
    })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: 'Command' }))
    await userEvent.type(screen.getByLabelText('Command'), 'python etl.py')
    await userEvent.type(screen.getByLabelText('Name'), 'Print the time')
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    await waitFor(() => expect(location()).toBe('/jobs/print-the-time'))
    expect(api.calls.find((c) => c.method === 'POST' && c.path === '/api/jobs')?.body).not.toHaveProperty('env')
  })

  it('shows a variable the server refuses under its row', async () => {
    base({
      'POST /api/jobs': {
        status: 422,
        body: { title: 'Unprocessable', errors: [{ location: 'body.env[0].name', message: 'JAVO_RUN is set by the platform. Pick another name.' }] },
      },
    })
    renderApp('/jobs/new')
    await userEvent.click(await screen.findByRole('radio', { name: 'Command' }))
    await userEvent.type(screen.getByLabelText('Command'), 'python etl.py')
    await userEvent.type(screen.getByLabelText('Name'), 'A')
    const card = screen.getByRole('heading', { name: 'Environment variables' }).closest('.part') as HTMLElement
    await userEvent.click(within(card).getByRole('button', { name: 'Add a variable' }))
    await userEvent.type(within(card).getByLabelText('Name'), 'JAVO_RUN')
    await userEvent.click(screen.getByRole('button', { name: 'Create job' }))
    expect(await screen.findByText('JAVO_RUN is set by the platform. Pick another name.')).toBeTruthy()
    expect(within(card).getByLabelText('Name').getAttribute('aria-invalid')).toBe('true')
    expect(screen.queryByText('Nothing was saved')).toBeNull()
  })

  it('offers no variables when editing: the job page changes them', async () => {
    base({ 'GET /api/jobs/weekly-report': { body: { ...weeklyDetail, env: [] } } })
    renderApp('/jobs/weekly-report/edit')
    await screen.findByRole('heading', { name: 'Edit Weekly report' })
    expect(screen.queryByRole('button', { name: 'Add a variable' })).toBeNull()
  })

  it('changes the variables of a job from its page, from the next run on', async () => {
    const detail = {
      ...weeklyDetail,
      env: [
        { name: 'PGPASSWORD', secret: 'WAREHOUSE_PASSWORD' },
        { name: 'REGION', value: 'emea' },
      ],
    }
    const api = base({
      'GET /api/jobs/weekly-report': { body: detail },
      'PUT /api/jobs/weekly-report/env': { body: { ...detail, env: [{ name: 'REGION', value: 'apac' }] } },
    })
    renderApp('/jobs/weekly-report')
    const term = await screen.findByText('Environment variables', { selector: 'dt' })
    expect(term.nextElementSibling?.textContent).toBe('PGPASSWORD from WAREHOUSE_PASSWORDREGION=emea')
    await userEvent.click(screen.getByRole('button', { name: 'Change variables' }))
    expect(screen.getByText('The next run gets the change; a run already going keeps what it started with.')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Save variables' }) as HTMLButtonElement).disabled).toBe(true)
    await userEvent.click(screen.getByRole('button', { name: 'Remove PGPASSWORD' }))
    const value = screen.getByLabelText('Value')
    await userEvent.clear(value)
    await userEvent.type(value, 'apac')
    await userEvent.click(screen.getByRole('button', { name: 'Save variables' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ env: [{ name: 'REGION', value: 'apac' }] }))
    expect(await screen.findByText('REGION=apac')).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Save variables' })).toBeNull()
  })
})

describe('Settings, Secrets with jobs', () => {
  it('names the jobs that use a secret next to the apps', async () => {
    base()
    renderApp('/settings/secrets')
    const list = await screen.findByRole('list', { name: 'Secrets' })
    const row = (name: string) => within(list).getAllByRole('listitem').find((r) => within(r).queryByRole('heading', { name }))!
    expect(within(row('WAREHOUSE_PASSWORD')).getByText('Used by').nextSibling?.textContent).toBe('Weekly report (job)')
    expect(within(row('OPENAI_KEY')).getByText('Used by').nextSibling?.textContent).toBe('Sales dashboard')
  })

  it('says jobs get a new value from their next run', async () => {
    base()
    renderApp('/settings/secrets')
    await userEvent.click(await screen.findByRole('button', { name: 'Replace the value of WAREHOUSE_PASSWORD' }))
    expect(screen.getByText('Jobs that use it get the new value from their next run.')).toBeTruthy()
  })
})
