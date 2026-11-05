import { render, screen } from '@testing-library/react'
import { QueryClientProvider } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { makeQueryClient } from '../../lib/queries'
import { mockApi } from '../../test/api'
import { SharedCard } from './SharedCard'

describe('SharedCard', () => {
  it('opens a running shared app in a new tab', () => {
    mockApi({ 'GET /api/templates': { body: [] } })
    render(
      <QueryClientProvider client={makeQueryClient()}>
        <SharedCard
          app={{
            displayName: 'Sales app', kind: 'streamlit', name: 'sales-app', template: 'streamlit', phase: 'Running',
            url: '/u/bob/sales-app/', via: 'user', owner: { id: 'u-2', displayName: 'Bob' } as never,
          }}
        />
      </QueryClientProvider>,
    )
    const link = screen.getByRole('link', { name: /Sales app/ })
    expect(link.getAttribute('href')).toBe('/u/bob/sales-app/')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(link.getAttribute('rel')).toBe('noopener')
  })

  it('says how the app is shared with the viewer', () => {
    mockApi({ 'GET /api/templates': { body: [] } })
    const cases: [string, string][] = [
      ['user', 'Shared with you'],
      ['group:/org/ml', 'Shared with your group ml'],
      ['signed-in', 'Shared with everyone signed in'],
      ['public', 'Anyone with the link'],
    ]
    for (const [via, words] of cases) {
      const { unmount } = render(
        <QueryClientProvider client={makeQueryClient()}>
          <SharedCard
            app={{
              displayName: 'Sales app', kind: 'streamlit', name: 'sales-app', template: 'streamlit', phase: 'Stopped',
              url: '/u/bob/sales-app/', via, owner: { id: 'u-2', displayName: 'Bob' } as never,
            }}
          />
        </QueryClientProvider>,
      )
      expect(screen.getByText(words)).toBeTruthy()
      unmount()
    }
  })

  it('shows its status as a named icon, not a text pill', () => {
    mockApi({ 'GET /api/templates': { body: [] } })
    const { container } = render(
      <QueryClientProvider client={makeQueryClient()}>
        <SharedCard
          app={{
            displayName: 'Sales app', kind: 'streamlit', name: 'sales-app', template: 'streamlit', phase: 'Stopped',
            url: '/u/bob/sales-app/', via: 'user', owner: { id: 'u-2', displayName: 'Bob' } as never,
          }}
        />
      </QueryClientProvider>,
    )
    expect(screen.getByRole('img', { name: 'Stopped' }).getAttribute('title')).toBe('Stopped')
    expect(container.querySelector('.pill')).toBeNull()
  })
})
