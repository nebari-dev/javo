import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { describe, expect, it, vi } from 'vitest'
import { UserMenu } from './UserMenu'

function setup() {
  const onSignOut = vi.fn()
  render(
    <MemoryRouter>
      <UserMenu name="Alice" onSignOut={onSignOut} />
      <p>Outside</p>
      <Routes>
        <Route path="/settings" element={<h1>Settings page</h1>} />
        <Route path="*" element={null} />
      </Routes>
    </MemoryRouter>,
  )
  return { onSignOut, trigger: screen.getByRole('button', { name: 'Account: Alice' }) }
}

describe('UserMenu', () => {
  it('opens a menu with the user name and Sign out', async () => {
    const { onSignOut, trigger } = setup()
    expect(trigger.getAttribute('aria-haspopup')).toBe('menu')
    expect(screen.queryByRole('menu')).toBeNull()
    await userEvent.click(trigger)
    expect(trigger.getAttribute('aria-expanded')).toBe('true')
    const menu = screen.getByRole('menu')
    expect(menu.textContent).toContain('Alice')
    await userEvent.click(screen.getByRole('menuitem', { name: 'Sign out' }))
    expect(onSignOut).toHaveBeenCalledOnce()
  })

  it('closes on Escape and returns focus to the avatar', async () => {
    const { trigger } = setup()
    await userEvent.click(trigger)
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: 'Settings' }))
    await userEvent.keyboard('{Escape}')
    expect(screen.queryByRole('menu')).toBeNull()
    expect(document.activeElement).toBe(trigger)
  })

  it('closes on a click outside', async () => {
    const { trigger } = setup()
    await userEvent.click(trigger)
    await userEvent.click(screen.getByText('Outside'))
    expect(screen.queryByRole('menu')).toBeNull()
  })

  it('lists Settings above Sign out and opens the settings page', async () => {
    const { trigger } = setup()
    await userEvent.click(trigger)
    expect(screen.getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Settings', 'Sign out'])
    await userEvent.click(screen.getByRole('menuitem', { name: 'Settings' }))
    expect(screen.getByRole('heading', { name: 'Settings page' })).toBeTruthy()
    expect(screen.queryByRole('menu')).toBeNull()
  })
})
