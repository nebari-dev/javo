import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { Tabs } from './Tabs'

// Each tab carries its icon; the icon must not change the tab's name.
const tabs = [
  { id: 'overview', label: 'Overview', icon: 'overview' as const, panel: <p>Overview body</p> },
  { id: 'sharing', label: 'Sharing', icon: 'sharing' as const, panel: <p>Sharing body</p> },
]

describe('Tabs', () => {
  it('shows the first panel and only the selected tab is in the tab order', () => {
    render(<Tabs label="App" tabs={tabs} />)
    expect(screen.getByText('Overview body')).toBeTruthy()
    expect(screen.queryByText('Sharing body')).toBeNull()
    expect(screen.getByRole('tab', { name: 'Overview' }).getAttribute('tabindex')).toBe('0')
    expect(screen.getByRole('tab', { name: 'Sharing' }).getAttribute('tabindex')).toBe('-1')
  })

  it('opens a tab on click', async () => {
    render(<Tabs label="App" tabs={tabs} />)
    await userEvent.click(screen.getByRole('tab', { name: 'Sharing' }))
    expect(screen.getByText('Sharing body')).toBeTruthy()
    expect(screen.getByRole('tab', { name: 'Sharing' }).getAttribute('aria-selected')).toBe('true')
  })

  it('moves with the arrow keys, wrapping, and Home and End', async () => {
    render(<Tabs label="App" tabs={tabs} />)
    screen.getByRole('tab', { name: 'Overview' }).focus()
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Sharing' }))
    expect(screen.getByText('Sharing body')).toBeTruthy()
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Overview' }))
    await userEvent.keyboard('{End}')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Sharing' }))
    await userEvent.keyboard('{Home}')
    expect(document.activeElement).toBe(screen.getByRole('tab', { name: 'Overview' }))
  })
})
