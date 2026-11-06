import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../test/api'
import { allReady, me, templates, ws } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

// Today at 14:30 local time: what the page shows as "14:30".
const at1430 = () => {
  const d = new Date()
  d.setHours(14, 30, 0, 0)
  return d.toISOString()
}

const pi = (trust: { allowed: boolean; until?: string } | undefined) =>
  ws('pi', 'Pi', 'Running', allReady(), { template: 'pi', kind: 'pi', file: '', apiAccess: 'ReadWrite', trust })

function page(...replies: object[]) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces/pi': replies.map((body) => ({ body })),
    'PUT /api/workspaces/pi/trust': (req) => ({
      body: pi(req.body && (req.body as { trusted: boolean }).trusted ? { allowed: true, until: at1430() } : { allowed: true }),
    }),
  })
}

describe('Letting a coding agent act without asking', () => {
  it('warns in plain words before it is turned on, then shows a banner until it is turned off', async () => {
    const api = page(pi({ allowed: true }))
    renderApp('/apps/pi')
    const toggle = await screen.findByRole('switch', { name: 'Let Pi act without asking' })
    expect(toggle.getAttribute('aria-checked')).toBe('false')
    await userEvent.click(toggle)

    const dialog = screen.getByRole('alertdialog', { name: 'Let Pi act without asking?' })
    const text = dialog.textContent ?? ''
    expect(text).toContain('It can do anything you can do in Javo')
    expect(text).toContain('start, stop and delete apps')
    expect(text).toContain('read and attach your secrets')
    expect(text).toContain('share apps')
    expect(text).toContain('If a web page, file or package tricks Pi, it can do those things for whoever tricked it.')
    expect(text).toMatch(/Until \d\d:\d\d( tomorrow)?, then it asks again\./)
    expect(text).toContain('Turn it off here any time.')
    expect(api.calls.some((c) => c.method === 'PUT')).toBe(false)

    await userEvent.click(within(dialog).getByRole('radio', { name: '1 hour' }))
    expect(within(dialog).queryByRole('button', { name: 'Let Pi act without asking for 8 hours' })).toBeNull()
    await userEvent.click(within(dialog).getByRole('radio', { name: '8 hours' }))
    const confirm = within(dialog).getByRole('button', { name: 'Let Pi act without asking for 8 hours' })
    expect(confirm.className).toContain('danger')
    await userEvent.click(confirm)

    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')?.body).toEqual({ trusted: true, hours: 8 }))
    const banner = await screen.findByRole('alert', { name: 'Pi acts without asking until 14:30' })
    expect(banner.textContent).toContain('It can do anything you can do in Javo')
    expect(screen.queryByRole('alertdialog')).toBeNull()

    await userEvent.click(within(banner).getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(api.calls.filter((c) => c.method === 'PUT').at(-1)?.body).toEqual({ trusted: false }))
    await waitFor(() => expect(screen.queryByRole('alert', { name: /acts without asking/ })).toBeNull())
    expect(screen.getByRole('switch', { name: 'Let Pi act without asking' }).getAttribute('aria-checked')).toBe('false')
  })

  it('changes nothing when the warning is cancelled', async () => {
    const api = page(pi({ allowed: true }))
    renderApp('/apps/pi')
    await userEvent.click(await screen.findByRole('switch', { name: 'Let Pi act without asking' }))
    await userEvent.click(within(screen.getByRole('alertdialog')).getByRole('button', { name: 'Keep asking' }))
    expect(screen.queryByRole('alertdialog')).toBeNull()
    expect(api.calls.some((c) => c.method === 'PUT')).toBe(false)
  })

  it('says the admin turned it off, and shows nothing on an app that is not a coding agent', async () => {
    page(pi({ allowed: false }))
    const first = renderApp('/apps/pi')
    expect(await screen.findByText('Your admin turned this off.')).toBeTruthy()
    expect(screen.queryByRole('switch', { name: 'Let Pi act without asking' })).toBeNull()
    first.unmount()
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/workspaces/pi': { body: { ...pi(undefined), kind: 'streamlit' } } })
    renderApp('/apps/pi')
    await screen.findByRole('heading', { level: 1 })
    expect(screen.queryByRole('switch', { name: /act without asking/ })).toBeNull()
  })

  it.each(['Cards', 'List'])('marks a trusted agent in the %s view with a Turn off button', async (view) => {
    const api = mockApi({
      'GET /api/me': { body: me },
      'GET /api/workspaces': [{ body: { items: [pi({ allowed: true, until: at1430() })] } }, { body: { items: [pi({ allowed: true })] } }],
      'GET /api/templates': { body: { items: templates } },
      'PUT /api/workspaces/pi/trust': { body: pi({ allowed: true }) },
    })
    renderApp('/apps')
    if (view === 'List') await userEvent.click(await screen.findByRole('button', { name: 'List' }))
    const badge = await screen.findByText('Acts without asking until 14:30')
    const holder = badge.closest('.trust-badge') as HTMLElement
    await userEvent.click(within(holder).getByRole('button', { name: 'Turn off' }))
    await waitFor(() => expect(api.calls.find((c) => c.method === 'PUT')).toMatchObject({ path: '/api/workspaces/pi/trust', body: { trusted: false } }))
    await waitFor(() => expect(screen.queryByText('Acts without asking until 14:30')).toBeNull())
  })
})
