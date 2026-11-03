import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Card } from './Card'

describe('Card', () => {
  it('renders children inside the card surface and merges extra classes', () => {
    render(
      <Card className="extra" data-testid="card">
        <span>Inside</span>
      </Card>,
    )
    const card = screen.getByTestId('card')
    expect(card.classList.contains('card')).toBe(true)
    expect(card.classList.contains('extra')).toBe(true)
    expect(screen.getByText('Inside')).toBeTruthy()
  })
})
