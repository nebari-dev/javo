import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ThemeSwitch } from './ThemeSwitch'

afterEach(() => {
  document.documentElement.removeAttribute('data-theme')
  vi.restoreAllMocks()
})

const button = () => screen.getByRole('button', { name: /^Appearance/ })

describe('ThemeSwitch', () => {
  it('is one button that names the current mode, system by default', () => {
    render(<ThemeSwitch />)
    expect(screen.getAllByRole('button')).toHaveLength(1)
    expect(button().getAttribute('aria-label')).toBe('Appearance: System')
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false)
  })

  it('cycles light, dark, then back to system, and persists the choice', async () => {
    render(<ThemeSwitch />)
    await userEvent.click(button())
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
    expect(button().getAttribute('aria-label')).toBe('Appearance: Light')
    await userEvent.click(button())
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
    expect(localStorage.getItem('javo.theme')).toBe('dark')
    await userEvent.click(button())
    expect(document.documentElement.hasAttribute('data-theme')).toBe(false)
    expect(button().getAttribute('aria-label')).toBe('Appearance: System')
  })

  it('restores the saved choice on mount', () => {
    localStorage.setItem('javo.theme', 'dark')
    render(<ThemeSwitch />)
    expect(button().getAttribute('aria-label')).toBe('Appearance: Dark')
    expect(document.documentElement.getAttribute('data-theme')).toBe('dark')
  })

  it('still works when storage throws', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    render(<ThemeSwitch />)
    await userEvent.click(button())
    expect(document.documentElement.getAttribute('data-theme')).toBe('light')
  })
})
