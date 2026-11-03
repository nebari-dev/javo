import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Plus } from 'lucide-react'
import { describe, expect, it, vi } from 'vitest'
import { Button } from './Button'

describe('Button', () => {
  it('renders an accessible button that fires onClick', async () => {
    const onClick = vi.fn()
    render(<Button onClick={onClick}>Save</Button>)
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(onClick).toHaveBeenCalledOnce()
  })

  it.each(['primary', 'ghost', 'danger'] as const)('applies the %s variant', (variant) => {
    render(<Button variant={variant}>Go</Button>)
    expect(screen.getByRole('button').classList.contains(variant)).toBe(true)
  })

  it('applies the small size', () => {
    render(<Button size="sm">Go</Button>)
    expect(screen.getByRole('button').classList.contains('sm')).toBe(true)
  })

  it('renders a decorative icon before the label', () => {
    render(<Button icon={Plus}>New app</Button>)
    const button = screen.getByRole('button', { name: 'New app' })
    const svg = button.querySelector('svg')
    expect(svg).not.toBeNull()
    expect(svg?.getAttribute('aria-hidden')).toBe('true')
  })

  it('does not fire onClick when disabled', async () => {
    const onClick = vi.fn()
    render(
      <Button disabled onClick={onClick}>
        Save
      </Button>,
    )
    await userEvent.click(screen.getByRole('button'))
    expect(onClick).not.toHaveBeenCalled()
  })

  it('defaults to type=button so it never submits a form by accident', () => {
    render(<Button>Go</Button>)
    expect(screen.getByRole('button').getAttribute('type')).toBe('button')
  })
})

describe('Button sizing', () => {
  it('marks icon-only buttons so they stay square instead of taking the minimum text width', () => {
    render(<Button icon={Plus} aria-label="Close" />)
    expect(screen.getByRole('button', { name: 'Close' }).classList.contains('icon')).toBe(true)
  })
  it('does not mark buttons that have a label', () => {
    render(<Button icon={Plus}>Close</Button>)
    expect(screen.getByRole('button', { name: 'Close' }).classList.contains('icon')).toBe(false)
  })
})
