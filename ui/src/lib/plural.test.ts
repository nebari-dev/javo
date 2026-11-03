import { describe, expect, it } from 'vitest'
import { count } from './plural'

describe('count', () => {
  it('uses the singular for one only', () => {
    expect([0, 1, 2].map((n) => count(n, 'type'))).toEqual(['0 types', '1 type', '2 types'])
  })
})
