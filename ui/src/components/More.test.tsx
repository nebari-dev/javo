import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { More } from './More'

function setup() {
  return render(
    <More closed="Who can help" open="Show less">
      <p>Ask an admin</p>
    </More>,
  )
}

describe('More', () => {
  it('hides children and shows the closed label until opened', () => {
    setup()
    const toggle = screen.getByRole('button', { name: 'Who can help' })
    expect(toggle.getAttribute('aria-expanded')).toBe('false')
    expect(screen.queryByText('Ask an admin')).toBeNull()
  })

  it('reveals children and swaps to the open label on click', async () => {
    setup()
    await userEvent.click(screen.getByRole('button', { name: 'Who can help' }))
    const toggle = screen.getByRole('button', { name: 'Show less' })
    expect(toggle.getAttribute('aria-expanded')).toBe('true')
    expect(screen.getByText('Ask an admin')).toBeTruthy()
  })

  it('toggles from the keyboard with Enter and Space', async () => {
    setup()
    await userEvent.tab()
    await userEvent.keyboard('{Enter}')
    expect(screen.getByText('Ask an admin')).toBeTruthy()
    await userEvent.keyboard(' ')
    expect(screen.queryByText('Ask an admin')).toBeNull()
  })

  it('rotates the chevron when open', async () => {
    const { container } = setup()
    expect(container.querySelector('.more-chevron')?.classList.contains('open')).toBe(false)
    await userEvent.click(screen.getByRole('button'))
    expect(container.querySelector('.more-chevron')?.classList.contains('open')).toBe(true)
  })

  it('links the toggle to the revealed region', async () => {
    setup()
    await userEvent.click(screen.getByRole('button'))
    const id = screen.getByRole('button').getAttribute('aria-controls')
    expect(id).toBeTruthy()
    expect(document.getElementById(id as string)?.textContent).toContain('Ask an admin')
  })

  it('can start open', () => {
    render(
      <More closed="More" open="Less" defaultOpen>
        <p>Shown</p>
      </More>,
    )
    expect(screen.getByText('Shown')).toBeTruthy()
  })
})
