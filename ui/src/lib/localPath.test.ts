import { describe, expect, it } from 'vitest'
import { safeNext } from './localPath'

// The same rule as the server's httpx.IsLocalPath: browsers drop tab, CR and
// LF from URLs and read a backslash as a slash, so those can name another host.
describe('safeNext', () => {
  it('keeps a path on this host', () => {
    expect(safeNext('/apps/sales?x=1#y')).toBe('/apps/sales?x=1#y')
    expect(safeNext('/api/auth/app-ticket?to=https%3A%2F%2Fapps.example.com%2F')).toBe('/api/auth/app-ticket?to=https%3A%2F%2Fapps.example.com%2F')
  })
  it.each([null, '', 'apps', '//evil.example/x', '/\\evil.example/x', '/\t/evil.example/x', '/\n/evil.example', '/\r/evil.example', 'https://evil.example/', '/a\\b'])(
    'refuses %j',
    (next) => {
      expect(safeNext(next)).toBe('/')
    },
  )
})
