import { act, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { LogView } from './LogView'

const empty = { title: 'No log lines yet', body: 'Lines show up here.' }

describe('LogView', () => {
  it('names the log in its header with the number of lines', () => {
    render(<LogView title="Run log" label="Run log" lines={[{ text: 'one' }, { text: 'two' }]} empty={empty} />)
    const view = screen.getByRole('region', { name: 'Run log' })
    expect(within(view).getByText('2 lines')).toBeTruthy()
    expect(within(view).getAllByRole('listitem')).toHaveLength(2)
  })

  it('says where the lines come from when told', () => {
    render(<LogView title="Run log" label="Run log" status="saved" lines={[{ text: 'one' }]} empty={empty} />)
    expect(screen.getByText('Saved log')).toBeTruthy()
  })

  it('copies the lines as plain text and says so', async () => {
    const user = userEvent.setup()
    render(<LogView title="App log" label="App log" lines={[{ text: 'one' }, { text: '\x1b[32mINFO\x1b[0m two' }]} empty={empty} />)
    await user.click(screen.getByRole('button', { name: 'Copy log' }))
    expect(await navigator.clipboard.readText()).toBe('one\nINFO two')
    expect(screen.getByRole('button', { name: 'Copied' })).toBeTruthy()
  })

  it('wraps long lines until asked not to', async () => {
    render(<LogView title="App log" label="App log" lines={[{ text: 'one' }]} empty={empty} />)
    const wrap = screen.getByRole('button', { name: 'Wrap lines' })
    expect(wrap.getAttribute('aria-pressed')).toBe('true')
    await userEvent.click(wrap)
    expect(wrap.getAttribute('aria-pressed')).toBe('false')
    expect(screen.getByRole('log').dataset.wrap).toBe('off')
  })

  it('shows the empty state, and nothing to copy, when there are no lines', () => {
    render(<LogView title="App log" label="App log" lines={[]} empty={empty} />)
    expect(screen.getByText('No log lines yet')).toBeTruthy()
    expect(screen.getByText('Lines show up here.')).toBeTruthy()
    expect((screen.getByRole('button', { name: 'Copy log' }) as HTMLButtonElement).disabled).toBe(true)
  })

  it('says it is loading, and shows a problem in place of the lines', () => {
    const { rerender } = render(<LogView title="Run log" label="Run log" lines={[]} loading empty={empty} />)
    expect(screen.getByText('Loading the log')).toBeTruthy()
    rerender(<LogView title="Run log" label="Run log" lines={[]} error="The log is gone." empty={empty} />)
    expect(screen.getByText('The log is gone.')).toBeTruthy()
    expect(screen.queryByText('No log lines yet')).toBeNull()
  })

  it('follows new lines until the user scrolls up, then offers Jump to latest', async () => {
    const lines = [{ id: 1, text: 'one' }]
    const { rerender } = render(<LogView title="App log" label="App log" status="live" follow lines={lines} empty={empty} />)
    const box = screen.getByRole('log')
    Object.defineProperty(box, 'scrollHeight', { configurable: true, get: () => 1000 })
    Object.defineProperty(box, 'clientHeight', { configurable: true, get: () => 100 })
    rerender(<LogView title="App log" label="App log" status="live" follow lines={[...lines, { id: 2, text: 'two' }]} empty={empty} />)
    expect(box.scrollTop).toBe(1000)
    expect(screen.queryByRole('button', { name: 'Jump to latest' })).toBeNull()
    act(() => {
      box.scrollTop = 200
      box.dispatchEvent(new Event('scroll'))
    })
    await userEvent.click(screen.getByRole('button', { name: 'Jump to latest' }))
    expect(box.scrollTop).toBe(1000)
  })
})
