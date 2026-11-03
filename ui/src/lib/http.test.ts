import { afterEach, describe, expect, it, vi } from 'vitest'
import { logout } from '../api/sdk.gen'
import './http'

afterEach(() => vi.unstubAllGlobals())

describe('API requests', () => {
  it('carry the header the gateway requires on state-changing calls', async () => {
    const seen: Request[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (input: RequestInfo | URL) => {
        seen.push(input as Request)
        return new Response(null, { status: 204 })
      }),
    )
    await logout()
    expect(seen).toHaveLength(1)
    expect(seen[0].headers.get('X-Javo-Request')).toBe('1')
  })
})
