import { describe, expect, it } from 'vitest'
import { ago } from './ago'

const now = Date.parse('2026-10-31T12:00:00Z')
const before = (seconds: number) => new Date(now - seconds * 1000).toISOString()

describe('ago', () => {
  it('says just now under a minute', () => expect(ago(before(20), now)).toBe('just now'))
  it('counts minutes, hours and days', () => {
    expect(ago(before(60), now)).toBe('1 minute ago')
    expect(ago(before(5 * 60), now)).toBe('5 minutes ago')
    expect(ago(before(3 * 3600), now)).toBe('3 hours ago')
    expect(ago(before(2 * 86400), now)).toBe('2 days ago')
  })
  it('says just now for a time a little ahead of the clock', () => expect(ago(before(-30), now)).toBe('just now'))
  it('gives an empty text for no or a broken time', () => {
    expect(ago(undefined, now)).toBe('')
    expect(ago('not a time', now)).toBe('')
  })
})
