import { describe, expect, it } from 'vitest'
import { dstNote as note, type DstWarning } from './dstNote'

// Days are kept on one line with no-break spaces; compare with plain ones.
const dstNote = (ws: DstWarning[], locale: string) => note(ws, locale).replace(/\u00a0/g, ' ')

const w = (code: DstWarning['code'], date: string, time: string, until = time, runs = 1): DstWarning => ({ code, date, time, until, runs, text: '' })

describe('dstNote', () => {
  it('says a daily run is skipped in spring and runs twice in autumn, with the days in words', () => {
    expect(dstNote([w('DSTSkipped', '2027-03-28', '01:30'), w('DSTRepeated', '2027-10-31', '01:30')], 'en-GB')).toBe(
      'Daylight saving: on Sun, 28 Mar 2027 the 01:30 run is skipped, and on Sun, 31 Oct 2027 it runs twice.',
    )
  })

  it('names the runs from first to last when several are affected', () => {
    expect(dstNote([w('DSTSkipped', '2027-03-28', '01:15', '01:45', 2), w('DSTRepeated', '2027-10-31', '01:15', '01:45', 2)], 'en-GB')).toBe(
      'Daylight saving: on Sun, 28 Mar 2027 the runs from 01:15 to 01:45 are skipped, and on Sun, 31 Oct 2027 they run twice.',
    )
  })

  it('words one change on its own, and a repeat first as in the southern hemisphere', () => {
    expect(dstNote([w('DSTRepeated', '2027-04-04', '02:30')], 'en-GB')).toBe('Daylight saving: on Sun, 4 Apr 2027 the 02:30 run happens twice.')
    expect(dstNote([w('DSTRepeated', '2027-04-04', '02:30'), w('DSTSkipped', '2027-10-03', '02:30')], 'en-GB')).toBe(
      'Daylight saving: on Sun, 4 Apr 2027 the 02:30 run happens twice, and on Sun, 3 Oct 2027 it is skipped.',
    )
  })

  it('formats the day in the reader locale', () => {
    expect(dstNote([w('DSTSkipped', '2027-03-28', '01:30')], 'en-US')).toBe('Daylight saving: on Sun, Mar 28, 2027 the 01:30 run is skipped.')
  })

  it('never breaks a line inside a day', () => {
    expect(note([w('DSTSkipped', '2027-03-28', '01:30')], 'en-GB')).toContain('Sun,\u00a028\u00a0Mar\u00a02027')
  })

  it('says nothing when there is nothing to say', () => {
    expect(dstNote([], 'en-GB')).toBe('')
  })
})
