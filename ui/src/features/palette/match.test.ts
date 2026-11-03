import { describe, expect, it } from 'vitest'
import { match } from './match'

const score = (text: string, q: string) => match(text, q)?.score ?? 0

describe('match', () => {
  it('ranks a prefix over a word start over a substring over initials', () => {
    const prefix = score('Sales app', 'sa')
    const word = score('Try the sample', 'sa')
    const sub = score('Visa card', 'sa')
    const initials = score('Streamlit app', 'sa')
    expect(prefix).toBeGreaterThan(word)
    expect(word).toBeGreaterThan(sub)
    expect(sub).toBeGreaterThan(initials)
    expect(initials).toBeGreaterThan(0)
  })

  it('does not match scattered letters', () => {
    expect(match('Research lab', 'sal')).toBeNull()
    expect(match('Orders API', 'sa')).toBeNull()
  })

  it('marks the original letters when accents are folded', () => {
    expect(match('Crème brûlée', 'brul')?.ranges).toEqual([[6, 10]])
  })

  it('matches query words that each start a word, in order', () => {
    expect(match('Try the Streamlit sample', 'try stream')?.ranges).toEqual([
      [0, 3],
      [8, 14],
    ])
  })
})
