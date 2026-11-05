import { screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { currentIdp, noteIdpHeader } from './idp'
import { mockApi, renderApp } from '../test/api'
import { me } from '../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

describe('sign-in service outage banner', () => {
  it('shows until when the user stays signed in, from the response header', async () => {
    const headers = {
      'Javo-Idp-Unreachable-Until': '2026-10-30T14:05:00Z',
      'Javo-Idp-Unreachable-Message': 'Sign-in service unreachable. You stay signed in until 14:05 UTC.',
    }
    mockApi({
      'GET /api/me': { body: me, headers },
      'GET /api/workspaces': { body: { items: [] }, headers },
      'GET /api/shared': { body: { items: [] }, headers },
      'GET /api/info': { body: { apiDocs: false, version: 'dev' }, headers },
    })
    renderApp('/')
    expect(await screen.findByText('Sign-in service unreachable. You stay signed in until 14:05 UTC.')).toBeTruthy()
  })

  it('is cleared by a response without the header', () => {
    const outage = { 'Javo-Idp-Unreachable-Until': '2026-10-30T14:05:00Z', 'Javo-Idp-Unreachable-Message': 'Down until 14:05 UTC.' }
    noteIdpHeader(new Headers(outage))
    expect(currentIdp()).toBe('Down until 14:05 UTC.')
    noteIdpHeader(new Headers())
    expect(currentIdp()).toBeNull()
  })
})
