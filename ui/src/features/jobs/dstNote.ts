import type { PreviewWarning } from '../../api'

export type DstWarning = PreviewWarning

/** "Sun 28 Mar 2027" in the reader's locale, kept on one line; the date is a day, so it is read in UTC. */
function day(date: string, locale?: string): string {
  const d = new Date(`${date}T12:00:00Z`)
  if (Number.isNaN(d.getTime())) return date
  return new Intl.DateTimeFormat(locale, { weekday: 'short', day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' })
    .format(d)
    .replace(/ /g, '\u00a0')
}

const skipped = (w: DstWarning) => w.code === 'DSTSkipped'

/** The runs, named in full: "the 01:30 run is skipped", "the runs from 01:15 to 01:45 happen twice". */
function full(w: DstWarning): string {
  const one = w.runs === 1
  const runs = one ? `the ${w.time} run` : `the runs from ${w.time} to ${w.until}`
  if (skipped(w)) return `${runs} ${one ? 'is' : 'are'} skipped`
  return `${runs} ${one ? 'happens' : 'happen'} twice`
}

/** The same runs again: "it runs twice", "they are skipped". */
function again(w: DstWarning): string {
  const one = w.runs === 1
  if (skipped(w)) return `${one ? 'it is' : 'they are'} skipped`
  return `${one ? 'it runs' : 'they run'} twice`
}

/**
 * One sentence for the year's clock changes, from the parts the preview API
 * sends: "Daylight saving: on Sun 28 Mar 2027 the 01:30 run is skipped, and
 * on Sun 31 Oct 2027 it runs twice." Empty when there are none.
 */
export function dstNote(warnings: DstWarning[], locale?: string): string {
  if (warnings.length === 0) return ''
  const parts = warnings.map((w, i) => {
    const prev = warnings[i - 1]
    const same = prev && prev.time === w.time && prev.until === w.until && prev.runs === w.runs
    return `on ${day(w.date, locale)} ${same ? again(w) : full(w)}`
  })
  return `Daylight saving: ${parts.join(', and ')}.`
}
