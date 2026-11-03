import { describe, expect, it } from 'vitest'
import { cronText } from './format'

describe('cronText', () => {
  it('reads times on the reader clock: 24 hour where the locale uses it', () => {
    expect(cronText('30 1 * * *', 'en-GB')).toBe('At 01:30')
    expect(cronText('0 18 * * 1', 'de-DE')).toBe('At 18:00, only on Monday')
  })

  it('and 12 hour where the locale uses that', () => {
    expect(cronText('30 1 * * *', 'en-US')).toBe('At 01:30 AM')
  })

  it('gives back an expression it cannot read', () => {
    expect(cronText('nope', 'en-GB')).toBe('nope')
  })
})
