import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { Avatar } from './Avatar'

describe('Avatar', () => {
  it('shows the picture when the user has one', () => {
    render(<Avatar name="Alice" picture="https://example.com/alice.png" />)
    const img = screen.getByRole('img', { name: 'Alice' })
    expect(img.tagName).toBe('IMG')
    expect(img.getAttribute('src')).toBe('https://example.com/alice.png')
  })

  it('draws a generated avatar when there is no picture', () => {
    render(<Avatar name="Alice" />)
    expect(screen.getByRole('img', { name: 'Alice' }).tagName.toLowerCase()).toBe('svg')
  })

  it('falls back to the generated avatar when the picture fails to load', () => {
    render(<Avatar name="Alice" picture="https://example.com/missing.png" />)
    fireEvent.error(screen.getByRole('img', { name: 'Alice' }))
    expect(screen.getByRole('img', { name: 'Alice' }).tagName.toLowerCase()).toBe('svg')
  })

  it('draws the same avatar for the same name and a different one for another name', () => {
    const art = (name: string) => {
      const { unmount } = render(<Avatar name={name} />)
      // Mask ids come from React's useId and differ per mount; the drawing does not.
      const html = screen.getByRole('img', { name }).innerHTML.replace(/_r_\w+_/g, 'id')
      unmount()
      return html
    }
    expect(art('Alice')).toBe(art('Alice'))
    expect(art('Alice')).not.toBe(art('Bob'))
  })
})
