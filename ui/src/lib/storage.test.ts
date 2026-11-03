import { afterEach, describe, expect, it, vi } from 'vitest'
import { readStored, writeStored } from './storage'

afterEach(() => vi.restoreAllMocks())

describe('storage', () => {
  it('round trips a value', () => {
    writeStored('k', 'v')
    expect(readStored('k')).toBe('v')
  })

  it('returns null when reading throws', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new Error('blocked')
    })
    expect(readStored('k')).toBeNull()
  })

  it('swallows write errors', () => {
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new Error('quota')
    })
    expect(() => writeStored('k', 'v')).not.toThrow()
  })
})
