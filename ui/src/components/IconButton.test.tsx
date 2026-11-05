import { act, fireEvent, render, screen } from '@testing-library/react'
import { Trash } from 'lucide-react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { IconButton } from './IconButton'

describe('IconButton tooltip', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  it('draws the tooltip on the page body, outside a clipping scroll box, and hides it on Escape', () => {
    render(
      <div data-testid="box" style={{ overflow: 'auto', height: 40 }}>
        <IconButton icon={Trash} label="Delete Sales" />
      </div>,
    )
    const button = screen.getByRole('button', { name: 'Delete Sales' })
    fireEvent.mouseEnter(button)
    act(() => vi.advanceTimersByTime(300))
    const tip = screen.getByRole('tooltip')
    expect(tip.textContent).toBe('Delete Sales')
    expect(tip.parentElement).toBe(document.body)
    expect(screen.getByTestId('box').contains(tip)).toBe(false)
    fireEvent.keyDown(button, { key: 'Escape' })
    expect(screen.queryByRole('tooltip')).toBeNull()
  })
})
