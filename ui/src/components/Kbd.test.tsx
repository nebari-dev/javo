import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Kbd } from './Kbd'

describe('Kbd', () => {
  it('renders a semantic kbd element', () => {
    const { container } = render(<Kbd>K</Kbd>)
    const el = container.querySelector('kbd')
    expect(el?.textContent).toBe('K')
  })
})
