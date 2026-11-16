import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { makeQueryClient } from '../../lib/queries'
import { mockApi, type Reply } from '../../test/api'
import { REASONS } from '../../test/fixtures'
import { HomePicker } from './HomePicker'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const starting: Reply = {
  status: 503,
  headers: { 'Retry-After': '2' },
  body: { status: 503, reason: 'HomeStarting', ...REASONS.HomeStarting },
}

function listing(names: string[], extra: Record<string, unknown> = {}): Reply {
  const entries = names.map((name) => ({ name, kind: 'file', size: 10, modified: '2026-10-30T10:00:00Z' }))
  return { body: { path: '', entries, count: entries.length, truncated: false, stale: false, ageSeconds: 0, ...extra } }
}

function renderPicker(over: Partial<Parameters<typeof HomePicker>[0]> = {}) {
  render(
    <QueryClientProvider client={makeQueryClient()}>
      <HomePicker extensions={['.py']} acceptReason="Streamlit runs .py files" onChoose={vi.fn()} onClose={vi.fn()} {...over} />
    </QueryClientProvider>,
  )
  return screen.getByRole('dialog', { name: over.folders ? 'Choose a folder' : 'Choose a file' })
}

/** A home with a folder and two files at the top, and one file in the folder. */
function home(c: { path: string }): Reply {
  const path = new URL(c.path, 'http://x').searchParams.get('path') ?? ''
  const at = (name: string, kind: string) => ({ name, kind, size: 10, modified: '2026-10-30T10:00:00Z' })
  const entries = path === 'sales' ? [at('app.py', 'file')] : [at('sales', 'folder'), at('notes.md', 'file'), at('main.py', 'file')]
  return { body: { path, entries, count: entries.length, truncated: false, stale: false, ageSeconds: 0 } }
}

const advance = (ms: number) => act(() => vi.advanceTimersByTimeAsync(ms))

describe('HomePicker', () => {
  it('shows a skeleton list while the home folder opens, then the list, with no banner on a quick open', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mockApi({ 'GET /api/files': [starting, listing(['app.py'])] })
    const picker = renderPicker()
    expect(within(picker).getByRole('status', { name: 'Loading this folder' })).toBeTruthy()
    await advance(100)
    expect(within(picker).getByRole('status', { name: 'Loading this folder' })).toBeTruthy()
    expect(within(picker).queryByText(REASONS.HomeStarting.title)).toBeNull()
    await advance(600)
    expect(await within(picker).findByRole('treeitem', { name: /app\.py/ })).toBeTruthy()
    expect(within(picker).queryByRole('status', { name: 'Loading this folder' })).toBeNull()
    expect(within(picker).queryByText(REASONS.HomeStarting.title)).toBeNull()
  })

  it('explains the wait only after 3 seconds, and keeps the same message while it polls', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    mockApi({ 'GET /api/files': starting })
    const picker = renderPicker()
    await advance(2500)
    expect(within(picker).queryByText(REASONS.HomeStarting.title)).toBeNull()
    await advance(700)
    const title = within(picker).getByText(REASONS.HomeStarting.title)
    expect(within(picker).getByText(REASONS.HomeStarting.what)).toBeTruthy()
    // Every poll answers 503 again: the message stays put, it is not taken down and put back.
    for (let i = 0; i < 6; i++) {
      await advance(500)
      expect(within(picker).getByText(REASONS.HomeStarting.title)).toBe(title)
      expect(within(picker).getByRole('status', { name: 'Loading this folder' })).toBeTruthy()
    }
  })

  it('backs off while it waits and stops asking once the folder is listed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false })
    const api = mockApi({ 'GET /api/files': [starting, starting, starting, starting, listing(['app.py'])] })
    const picker = renderPicker()
    const calls = () => api.count('GET', '/api/files')
    await advance(0)
    expect(calls()).toBe(1)
    await advance(500)
    expect(calls()).toBe(2)
    await advance(900)
    expect(calls()).toBe(2)
    await advance(100)
    expect(calls()).toBe(3)
    await advance(1900)
    expect(calls()).toBe(3)
    await advance(100)
    expect(calls()).toBe(4)
    await advance(2000)
    expect(calls()).toBe(5)
    await advance(10)
    expect(within(picker).getByRole('treeitem', { name: /app\.py/ })).toBeTruthy()
    await advance(10000)
    expect(calls()).toBe(5)
  })

  it('shows the last listing at once, marked as updating, and swaps in the fresh one in place', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const api = mockApi({ 'GET /api/files': [listing(['old.py'], { stale: true, ageSeconds: 40 }), listing(['old.py', 'new.py'])] })
    const picker = renderPicker()
    const old = await within(picker).findByRole('treeitem', { name: /old\.py/ })
    expect(within(picker).getByRole('status', { name: 'Updating this folder' })).toBeTruthy()
    expect(within(picker).queryByRole('status', { name: 'Loading this folder' })).toBeNull()
    await advance(600)
    expect(await within(picker).findByRole('treeitem', { name: /new\.py/ })).toBeTruthy()
    // The row already shown is the same element, updated in place.
    expect(within(picker).getByRole('treeitem', { name: /old\.py/ })).toBe(old)
    expect(within(picker).queryByRole('status', { name: 'Updating this folder' })).toBeNull()
    await advance(10000)
    expect(api.count('GET', '/api/files')).toBe(2)
  })

  it('shows a failure in place of the list', async () => {
    mockApi({ 'GET /api/files': { status: 409, body: { status: 409, reason: 'HomeBusy', ...REASONS.HomeBusy } } })
    const picker = renderPicker()
    const card = await within(picker).findByRole('alert')
    expect(within(card).getByText(REASONS.HomeBusy.title)).toBeTruthy()
    expect(within(picker).queryByRole('status', { name: 'Loading this folder' })).toBeNull()
  })

  it('opens folders in place and chooses an accepted file, with the others dimmed and saying why', async () => {
    mockApi({ 'GET /api/files': home })
    const onChoose = vi.fn()
    const picker = renderPicker({ onChoose })
    const notes = await within(picker).findByRole('treeitem', { name: 'notes.md' })
    expect(notes.getAttribute('aria-disabled')).toBe('true')
    expect(notes.getAttribute('title')).toBe('Streamlit runs .py files')
    await userEvent.click(within(picker).getByRole('treeitem', { name: 'sales' }))
    await userEvent.click(await within(picker).findByRole('treeitem', { name: 'app.py' }))
    expect(within(picker).getByText('sales/app.py')).toBeTruthy()
    await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
    expect(onChoose).toHaveBeenCalledWith('sales/app.py')
  })

  it('narrows the rows by name, and says so when nothing matches', async () => {
    mockApi({ 'GET /api/files': home })
    const picker = renderPicker()
    await within(picker).findByRole('treeitem', { name: 'main.py' })
    await userEvent.type(within(picker).getByRole('searchbox', { name: 'Search by name' }), 'main')
    expect(within(picker).getAllByRole('treeitem').map((r) => r.getAttribute('aria-label'))).toEqual(['main.py'])
    await userEvent.type(within(picker).getByRole('searchbox', { name: 'Search by name' }), 'zzz')
    expect(within(picker).getByText('No files match your search')).toBeTruthy()
  })

  it('chooses the home folder itself in folder mode until a folder is picked', async () => {
    mockApi({ 'GET /api/files': home })
    const onChoose = vi.fn()
    const picker = renderPicker({ folders: true, onChoose })
    await within(picker).findByRole('treeitem', { name: 'sales' })
    expect(within(picker).getByText('Home folder')).toBeTruthy()
    await userEvent.click(within(picker).getByRole('button', { name: 'Choose' }))
    expect(onChoose).toHaveBeenCalledWith('')
  })

  it('shows an error with Retry, which asks again', async () => {
    const api = mockApi({ 'GET /api/files': [{ status: 500, body: { title: 'The home folder could not be read.' } }, { body: { path: '', entries: [], count: 0, truncated: false, stale: false, ageSeconds: 0 } }] })
    const picker = renderPicker()
    expect(await within(picker).findByText('The home folder could not be read.')).toBeTruthy()
    await userEvent.click(within(picker).getByRole('button', { name: 'Retry' }))
    await waitFor(() => expect(api.count('GET', '/api/files')).toBe(2))
    expect(await within(picker).findByText('This folder is empty')).toBeTruthy()
  })
})
