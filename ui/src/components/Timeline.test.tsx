import { render, screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Timeline, type TimelineStep } from './Timeline'

const steps: TimelineStep[] = [
  { title: 'Request accepted', detail: 'Saved your request', time: '0 s', state: 'done' },
  { title: 'Finding a node', detail: 'Waiting for capacity', time: '4 s', state: 'now' },
  { title: 'Starting the app', state: 'bad', detail: 'Out of memory' },
  { title: 'Ready', state: 'todo' },
]

describe('Timeline', () => {
  it('lists every step in order with times and detail', () => {
    render(<Timeline steps={steps} />)
    const items = screen.getAllByRole('listitem')
    expect(items).toHaveLength(4)
    expect(within(items[0]).getByText('Request accepted')).toBeTruthy()
    expect(within(items[0]).getByText('0 s')).toBeTruthy()
    expect(within(items[1]).getByText('Waiting for capacity')).toBeTruthy()
  })

  it('marks the current step and gives every state a text label', () => {
    render(<Timeline steps={steps} />)
    const items = screen.getAllByRole('listitem')
    expect(items[1].getAttribute('aria-current')).toBe('step')
    expect(items[0].getAttribute('aria-current')).toBeNull()
    expect(within(items[0]).getByText('Done')).toBeTruthy()
    expect(within(items[1]).getByText('In progress')).toBeTruthy()
    expect(within(items[2]).getByText('Failed')).toBeTruthy()
    expect(within(items[3]).getByText('Not started')).toBeTruthy()
  })

  it('exposes the state for styling', () => {
    render(<Timeline steps={steps} />)
    expect(screen.getAllByRole('listitem').map((li) => li.getAttribute('data-state'))).toEqual([
      'done',
      'now',
      'bad',
      'todo',
    ])
  })
})
