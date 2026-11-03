import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { allReady, stepsAt, withSince, ws } from '../../test/fixtures'
import { Uptime } from './Uptime'

const NOW = '2026-11-03T12:00:00.000Z'
const ago = (seconds: number) => new Date(Date.parse(NOW) - seconds * 1000).toISOString()
/** A running app whose Ready step turned True `seconds` before now. */
const running = (seconds: number) => ws('sales', 'Sales', 'Running', withSince(allReady(), ago(seconds), [0, 0, 0, 0, 0]))

describe('Uptime', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    vi.setSystemTime(NOW)
  })
  afterEach(() => vi.useRealTimers())

  it('shows how long a running app has been up, from the Ready time the API sent', () => {
    render(<Uptime ws={running(2 * 3600 + 14 * 60)} />)
    const t = screen.getByText('2 h 14 min')
    expect(t.tagName).toBe('TIME')
    expect(t.getAttribute('dateTime')).toBe(ago(2 * 3600 + 14 * 60))
    expect(t.getAttribute('title')).toMatch(/^Running since /)
  })

  it('ticks each second under a minute, then each minute', () => {
    render(<Uptime ws={running(58)} />)
    expect(screen.getByText('58 s')).toBeTruthy()
    act(() => vi.advanceTimersByTime(1000))
    expect(screen.getByText('59 s')).toBeTruthy()
    act(() => vi.advanceTimersByTime(1000))
    expect(screen.getByText('1 min')).toBeTruthy()
    act(() => vi.advanceTimersByTime(59_000))
    expect(screen.getByText('1 min')).toBeTruthy()
    act(() => vi.advanceTimersByTime(1000))
    expect(screen.getByText('2 min')).toBeTruthy()
  })

  it('stops its timer when it goes away', () => {
    const { unmount } = render(<Uptime ws={running(10)} />)
    unmount()
    expect(vi.getTimerCount()).toBe(0)
  })

  it('shows nothing for an app that is starting or stopped', () => {
    const { container, rerender } = render(<Uptime ws={ws('a', 'A', 'Starting', stepsAt(2, 'HomeStarting'))} />)
    expect(container.textContent).toBe('')
    rerender(<Uptime ws={ws('a', 'A', 'Stopped', withSince(allReady(), ago(60), [0, 0, 0, 0, 0]))} />)
    expect(container.textContent).toBe('')
    expect(vi.getTimerCount()).toBe(0)
  })

  it('shows nothing for a running app without a Ready time', () => {
    const { container } = render(<Uptime ws={ws('a', 'A', 'Running', allReady())} />)
    expect(container.textContent).toBe('')
  })
})
