import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, within } from '@testing-library/react'
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

function renderPicker() {
  render(
    <QueryClientProvider client={makeQueryClient()}>
      <HomePicker extensions={['.py']} acceptReason="Streamlit runs .py files" onChoose={vi.fn()} onClose={vi.fn()} />
    </QueryClientProvider>,
  )
  return screen.getByRole('dialog', { name: 'Choose a file' })
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
    expect(await within(picker).findByRole('button', { name: /app\.py/ })).toBeTruthy()
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
    expect(within(picker).getByRole('button', { name: /app\.py/ })).toBeTruthy()
    await advance(10000)
    expect(calls()).toBe(5)
  })

  it('shows the last listing at once, marked as updating, and swaps in the fresh one in place', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const api = mockApi({ 'GET /api/files': [listing(['old.py'], { stale: true, ageSeconds: 40 }), listing(['old.py', 'new.py'])] })
    const picker = renderPicker()
    const old = await within(picker).findByRole('button', { name: /old\.py/ })
    expect(within(picker).getByRole('status', { name: 'Updating this folder' })).toBeTruthy()
    expect(within(picker).queryByRole('status', { name: 'Loading this folder' })).toBeNull()
    await advance(600)
    expect(await within(picker).findByRole('button', { name: /new\.py/ })).toBeTruthy()
    // The row already shown is the same element, updated in place.
    expect(within(picker).getByRole('button', { name: /old\.py/ })).toBe(old)
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
})
