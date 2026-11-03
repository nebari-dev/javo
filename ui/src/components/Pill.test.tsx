import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Pill } from './Pill'

describe('Pill', () => {
  it.each([
    ['running', 'Running'],
    ['starting', 'Starting'],
    ['pending', 'Pending'],
    ['failed', 'Failed'],
    ['stopped', 'Stopped'],
  ] as const)('shows a text label for %s so status is never color only', (status, label) => {
    render(<Pill status={status} />)
    expect(screen.getByText(label)).toBeTruthy()
  })

  it('hides the decorative dot from assistive tech', () => {
    const { container } = render(<Pill status="running" />)
    expect(container.querySelector('.dot')?.getAttribute('aria-hidden')).toBe('true')
  })

  it('lets the caller override the label', () => {
    render(<Pill status="pending" label="Waiting for a node" />)
    expect(screen.getByText('Waiting for a node')).toBeTruthy()
    expect(screen.queryByText('Pending')).toBeNull()
  })
})
