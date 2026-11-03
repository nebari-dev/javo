import { QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { makeQueryClient } from '../../lib/queries'
import { mockApi } from '../../test/api'
import { FakeEventSource } from '../../test/eventsource'
import { LiveLogs } from './LiveLogs'

afterEach(() => vi.unstubAllGlobals())

function renderLogs(restarts = 0) {
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <LiveLogs name="sales-app" restarts={restarts} />
    </QueryClientProvider>,
  )
}

const line = (s: number, text: string) => ({ time: `2026-10-30T12:00:${String(s).padStart(2, '0')}Z`, text })

describe('LiveLogs', () => {
  it('follows the stream and appends each line it sends', () => {
    renderLogs()
    const es = FakeEventSource.latest()
    expect(es.url).toBe('/api/workspaces/sales-app/logs/stream')
    act(() => {
      es.open()
      es.emit('line', line(1, 'You can now view your Streamlit app'))
      es.emit('line', line(2, '<i>literal</i>'))
    })
    const items = within(screen.getByRole('list', { name: 'App log' })).getAllByRole('listitem')
    expect(items.map((i) => i.textContent)).toEqual([
      expect.stringContaining('You can now view your Streamlit app'),
      expect.stringContaining('<i>literal</i>'),
    ])
  })

  it('starts over when the stream reconnects, since the server replays the latest lines', () => {
    renderLogs()
    const es = FakeEventSource.latest()
    act(() => {
      es.open()
      es.emit('line', line(1, 'first run'))
      es.open()
      es.emit('line', line(1, 'first run'))
    })
    expect(screen.getAllByText('first run')).toHaveLength(1)
  })

  it('says so when there are no lines yet', () => {
    renderLogs()
    expect(screen.getByText('No log lines yet')).toBeTruthy()
  })

  it('closes the stream when it goes away', () => {
    const { unmount } = renderLogs()
    const es = FakeEventSource.latest()
    unmount()
    expect(es.closed).toBe(true)
  })

  it('keeps following the newest line until the user scrolls up, then offers Jump to latest', async () => {
    renderLogs()
    const es = FakeEventSource.latest()
    act(() => es.open())
    const box = screen.getByRole('log')
    Object.defineProperty(box, 'scrollHeight', { configurable: true, get: () => 1000 })
    Object.defineProperty(box, 'clientHeight', { configurable: true, get: () => 100 })

    act(() => es.emit('line', line(1, 'one')))
    expect(box.scrollTop).toBe(1000)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()

    act(() => {
      box.scrollTop = 200
      box.dispatchEvent(new Event('scroll'))
    })
    act(() => es.emit('line', line(2, 'two')))
    expect(box.scrollTop).toBe(200)

    await userEvent.click(screen.getByRole('button', { name: 'Jump to latest' }))
    expect(box.scrollTop).toBe(1000)
    act(() => es.emit('line', line(3, 'three')))
    expect(box.scrollTop).toBe(1000)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()
  })

  it('copies the shown lines as plain text, without colour codes', async () => {
    const user = userEvent.setup()
    renderLogs()
    const es = FakeEventSource.latest()
    act(() => {
      es.open()
      es.emit('line', line(1, 'one'))
      es.emit('line', line(2, '\x1b[32mINFO\x1b[0m:     two'))
    })
    await user.click(screen.getByRole('button', { name: 'Copy log' }))
    expect(await navigator.clipboard.readText()).toBe('one\nINFO:     two')
  })

  it('shows the run before the last restart on request', async () => {
    const api = mockApi({
      'GET /api/workspaces/sales-app/logs': { body: { lines: [line(1, 'Traceback (most recent call last):')], restarts: 1 } },
    })
    renderLogs(1)
    await userEvent.click(screen.getByRole('button', { name: 'Show previous run' }))
    const list = await screen.findByRole('list', { name: 'Previous run log' })
    expect(list.textContent).toContain('Traceback (most recent call last):')
    await waitFor(() => expect(api.calls.some((c) => c.path.includes('previous=true'))).toBe(true))
  })

  it('offers no previous run when the app never restarted', () => {
    renderLogs(0)
    expect(screen.queryByRole('button', { name: 'Show previous run' })).toBeNull()
  })
})
