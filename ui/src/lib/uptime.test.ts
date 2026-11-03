import { describe, expect, it } from 'vitest'
import { untilNextChange, uptime } from './uptime'

const S = 1000
const MIN = 60 * S
const H = 60 * MIN
const D = 24 * H

describe('uptime', () => {
  it('counts seconds under a minute', () => {
    expect(uptime(0)).toBe('0 s')
    expect(uptime(45 * S + 900)).toBe('45 s')
  })
  it('counts minutes under an hour', () => {
    expect(uptime(MIN)).toBe('1 min')
    expect(uptime(12 * MIN + 59 * S)).toBe('12 min')
  })
  it('counts hours and minutes under a day', () => {
    expect(uptime(2 * H + 14 * MIN)).toBe('2 h 14 min')
    expect(uptime(5 * H)).toBe('5 h')
  })
  it('counts days and hours', () => {
    expect(uptime(3 * D + 4 * H + 30 * MIN)).toBe('3 d 4 h')
    expect(uptime(2 * D + 10 * MIN)).toBe('2 d')
  })
  it('says 0 s for a start a little ahead of the clock', () => expect(uptime(-5 * S)).toBe('0 s'))
})

describe('untilNextChange', () => {
  it('waits for the next second under a minute', () => {
    expect(untilNextChange(10 * S + 300)).toBe(700)
    expect(untilNextChange(-200)).toBe(200)
  })
  it('waits for the next minute after that', () => {
    expect(untilNextChange(MIN)).toBe(MIN)
    expect(untilNextChange(2 * H + 59 * MIN + 50 * S)).toBe(10 * S)
  })
})
