import Anser from 'anser'

/** A run of text with the colours and styles an app gave it with ANSI codes. */
export interface AnsiRun {
  text: string
  /** Theme classes for the named colours and styles, e.g. "ansi-red ansi-bold". */
  className?: string
  /** Colours outside the 16 named ones, as CSS. */
  style?: { color?: string; backgroundColor?: string }
}

const NAMES = ['black', 'red', 'green', 'yellow', 'blue', 'magenta', 'cyan', 'white']

// Control characters anser leaves in the text (such as charset switches); never shown.
const CONTROL = /\u001b[()][0-9A-Za-z]|[\u0000-\u0008\u000b-\u001f\u007f]/g

// Characters that reorder or hide text: bidi embeddings, overrides and
// isolates, zero-width spaces and joiners, word joiners and the BOM. A line
// could use them to show something other than what it says; each is shown
// as a visible marker such as <U+202E> instead.
const INVISIBLE = /[\u061c\u200b-\u200f\u202a-\u202e\u2060-\u2064\u2066-\u206f\ufeff]/g

const marker = (c: string) => `<U+${c.codePointAt(0)!.toString(16).toUpperCase().padStart(4, '0')}>`

const clean = (s: string) => s.replace(CONTROL, '').replace(INVISIBLE, marker)

/** Bright colours share their base colour's theme token; palette 0 to 15 are the named ones. */
function named(cls: string | null): string | undefined {
  if (!cls) return undefined
  const m = cls.match(/^ansi-(?:bright-)?(\w+)$/)
  if (m && NAMES.includes(m[1])) return m[1]
  const p = cls.match(/^ansi-palette-(\d+)$/)
  if (p && Number(p[1]) < 16) return NAMES[Number(p[1]) % 8]
  return undefined
}

const runs = new Map<string, AnsiRun[] | null>()
const RUNS_MAX = 5000

/** The coloured runs of a line, or null when it has no ANSI codes. */
export function ansiRuns(text: string): AnsiRun[] | null {
  if (!text.includes('\u001b')) return null
  const hit = runs.get(text)
  if (hit !== undefined) return hit
  const classed = Anser.ansiToJson(text, { use_classes: true, remove_empty: true })
  // The same parse with colours as RGB, for palette and true colours with no theme token.
  const rgb = Anser.ansiToJson(text, { remove_empty: true })
  const out: AnsiRun[] = []
  classed.forEach((e, i) => {
    const t = clean(e.content)
    if (!t) return
    const classes: string[] = []
    const style: AnsiRun['style'] = {}
    const fg = named(e.fg)
    const bg = named(e.bg)
    if (fg) classes.push(`ansi-${fg}`)
    else if (e.fg && rgb[i]?.fg) style.color = `rgb(${rgb[i].fg})`
    if (bg) classes.push(`ansi-bg-${bg}`)
    else if (e.bg && rgb[i]?.bg) style.backgroundColor = `rgb(${rgb[i].bg})`
    for (const d of e.decorations) if (d === 'bold' || d === 'dim' || d === 'italic' || d === 'underline') classes.push(`ansi-${d}`)
    const run: AnsiRun = { text: t }
    if (classes.length) run.className = classes.join(' ')
    if (style.color || style.backgroundColor) run.style = style
    out.push(run)
  })
  if (runs.size >= RUNS_MAX) runs.delete(runs.keys().next().value!)
  runs.set(text, out)
  return out
}

/** The line as the reader sees it: no ANSI codes or other control characters. */
export function plainText(text: string): string {
  const r = ansiRuns(text)
  return r ? r.map((x) => x.text).join('') : clean(text)
}
