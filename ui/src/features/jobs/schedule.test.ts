import { describe, expect, it } from 'vitest'
import { fromCron, toCron, type Schedule } from './schedule'

describe('toCron', () => {
  it.each<[Schedule, string]>([
    [{ kind: 'minute' }, '* * * * *'],
    [{ kind: 'minutes', every: 15 }, '*/15 * * * *'],
    [{ kind: 'hourly', minute: 30 }, '30 * * * *'],
    [{ kind: 'daily', hour: 6, minute: 5 }, '5 6 * * *'],
    [{ kind: 'weekly', days: [1], hour: 6, minute: 0 }, '0 6 * * 1'],
    [{ kind: 'weekly', days: [5, 1, 3], hour: 18, minute: 45 }, '45 18 * * 1,3,5'],
    [{ kind: 'weekly', days: [1, 2, 3, 4, 5], hour: 9, minute: 0 }, '0 9 * * 1-5'],
    [{ kind: 'weekly', days: [0, 6], hour: 9, minute: 0 }, '0 9 * * 0,6'],
    [{ kind: 'weekly', days: [0, 1, 2, 4], hour: 9, minute: 0 }, '0 9 * * 0-2,4'],
    [{ kind: 'monthly', day: 1, hour: 0, minute: 0 }, '0 0 1 * *'],
    [{ kind: 'custom', cron: '  0 6 1-7 * 1 ' }, '0 6 1-7 * 1'],
  ])('%j is %s', (s, cron) => {
    expect(toCron(s)).toBe(cron)
  })

  it('writes nothing for a weekly schedule with no day, so the form can ask for one', () => {
    expect(toCron({ kind: 'weekly', days: [], hour: 6, minute: 0 })).toBe('')
  })
})

describe('fromCron', () => {
  it.each<[string, Schedule]>([
    ['* * * * *', { kind: 'minute' }],
    ['*/5 * * * *', { kind: 'minutes', every: 5 }],
    ['7 * * * *', { kind: 'hourly', minute: 7 }],
    ['0 6 * * *', { kind: 'daily', hour: 6, minute: 0 }],
    ['00 06 * * *', { kind: 'daily', hour: 6, minute: 0 }],
    ['0 6 * * 1', { kind: 'weekly', days: [1], hour: 6, minute: 0 }],
    ['30 8 * * 1-5', { kind: 'weekly', days: [1, 2, 3, 4, 5], hour: 8, minute: 30 }],
    ['30 8 * * 5,1,3', { kind: 'weekly', days: [1, 3, 5], hour: 8, minute: 30 }],
    ['30 8 * * 7', { kind: 'weekly', days: [0], hour: 8, minute: 30 }],
    ['30 8 * * MON,fri', { kind: 'weekly', days: [1, 5], hour: 8, minute: 30 }],
    ['0 0 15 * *', { kind: 'monthly', day: 15, hour: 0, minute: 0 }],
    ['\t0  6 *  * *\n', { kind: 'daily', hour: 6, minute: 0 }],
  ])('reads %j back as its preset', (cron, s) => {
    expect(fromCron(cron)).toEqual(s)
  })

  it.each([
    '*/7 * * * *', // not one of the offered intervals
    '0 6 1 1 *', // a month field
    '0 6 1 * 1', // day of month and weekday together
    '0 */2 * * *', // every other hour
    '60 * * * *', // out of range
    '0 24 * * *',
    '0 0 32 * *',
    '0 6 * * 8',
    '0 6 * * 5-1',
    '0 6 * *', // four fields
    '@daily',
    '0 6 * * 1 2026',
    '',
  ])('keeps %j as a custom expression', (cron) => {
    expect(fromCron(cron)).toEqual({ kind: 'custom', cron: cron.trim() })
  })

  it('round-trips every preset', () => {
    const all: Schedule[] = [
      { kind: 'minute' },
      { kind: 'minutes', every: 30 },
      { kind: 'hourly', minute: 59 },
      { kind: 'daily', hour: 23, minute: 59 },
      { kind: 'weekly', days: [0, 2, 3, 4, 6], hour: 12, minute: 0 },
      { kind: 'monthly', day: 31, hour: 1, minute: 2 },
    ]
    for (const s of all) expect(fromCron(toCron(s))).toEqual(s)
  })
})
