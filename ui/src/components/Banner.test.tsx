import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Banner } from './Banner'

describe('Banner', () => {
  it('renders title, body and actions', () => {
    render(
      <Banner tone="info" title="Heads up" actions={<button type="button">Retry</button>}>
        Capacity is being added.
      </Banner>,
    )
    expect(screen.getByText('Heads up')).toBeTruthy()
    expect(screen.getByText('Capacity is being added.')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy()
  })

  it('announces errors as alerts and the rest as status', () => {
    const { rerender } = render(<Banner tone="err" title="Failed" />)
    expect(screen.getByRole('alert')).toBeTruthy()
    rerender(<Banner tone="warn" title="Slow" />)
    expect(screen.getByRole('status')).toBeTruthy()
    rerender(<Banner tone="info" title="FYI" />)
    expect(screen.getByRole('status')).toBeTruthy()
  })

  it('shows a decorative icon for each tone', () => {
    const { container } = render(<Banner tone="warn" title="Slow" />)
    expect(container.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true')
  })
})
