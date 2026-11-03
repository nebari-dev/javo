import { render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { EmptyState } from './EmptyState'

describe('EmptyState', () => {
  it('renders the headline, body and an action link', () => {
    render(
      <MemoryRouter>
        <EmptyState title="Nothing yet" body="Add one to get going." action={{ label: 'Add one', to: '/add' }} />
      </MemoryRouter>,
    )
    expect(screen.getByRole('heading', { name: 'Nothing yet' })).toBeTruthy()
    expect(screen.getByText('Add one to get going.')).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Add one' }).getAttribute('href')).toBe('/add')
  })

  it('leaves out the action when none is given and hides the illustration from screen readers', () => {
    const { container } = render(<EmptyState title="Empty folder" body="Pick another folder." />)
    expect(screen.queryByRole('link')).toBeNull()
    expect(container.querySelector('svg')?.getAttribute('aria-hidden')).toBe('true')
  })
})
