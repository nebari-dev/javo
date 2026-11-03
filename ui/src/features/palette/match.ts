/** A match: its rank (higher is better) and the matched [start, end) ranges of the text. */
interface Match {
  score: number
  ranges: [number, number][]
}

interface Folded {
  /** Lower case, accents removed. */
  text: string
  /** For each character of `text`, its index in the original. */
  at: number[]
  /** Indexes in `text` where a word starts. */
  starts: number[]
}

const isWordChar = (c: string) => {
  const n = c.charCodeAt(0)
  if (n < 128) return (n >= 48 && n <= 57) || (n >= 65 && n <= 90) || (n >= 97 && n <= 122)
  return /[\p{L}\p{N}]/u.test(c)
}

// Labels repeat on every keystroke, so their folded form is kept (bounded).
const folded = new Map<string, Folded>()

function fold(original: string): Folded {
  let f = folded.get(original)
  if (!f) {
    if (folded.size > 5000) folded.clear()
    f = foldNow(original)
    folded.set(original, f)
  }
  return f
}

function foldNow(original: string): Folded {
  let text = ''
  const at: number[] = []
  const starts: number[] = []
  let prev = ''
  for (let i = 0; i < original.length; i++) {
    const c = original[i]
    const f = c.charCodeAt(0) < 128 ? c.toLowerCase() : c.normalize('NFD').replace(/\p{M}/gu, '').toLowerCase()
    // A word starts after a separator, or at a capital after a lower case letter (JupyterLab).
    if (isWordChar(c) && (!isWordChar(prev) || (prev !== prev.toUpperCase() && c !== c.toLowerCase())))
      starts.push(text.length)
    text += f
    for (let k = 0; k < f.length; k++) at.push(i)
    prev = c
  }
  return { text, at, starts }
}

const foldQuery = (q: string) => foldNow(q.trim().replace(/\s+/g, ' ')).text

// Ranks, best first. Within a rank, an earlier and a shorter match wins.
const PREFIX = 1000
const WORD = 800
const SUBSTRING = 600
const WORDS = 500
const INITIALS = 400

function span(f: Folded, from: number, length: number): [number, number] {
  return [f.at[from], f.at[from + length - 1] + 1]
}

// Every query word starts a word of the text, in order ("try streamlit").
function words(f: Folded, q: string): [number, number][] | null {
  const ranges: [number, number][] = []
  let w = 0
  for (const part of q.split(' ')) {
    while (w < f.starts.length && !f.text.startsWith(part, f.starts[w])) w++
    if (w === f.starts.length) return null
    ranges.push(span(f, f.starts[w], part.length))
    w++
  }
  return ranges
}

// The query is made of word beginnings, in order: "sf" or "salfo" for Sales forecast.
function initials(f: Folded, q: string, from = 0, w = 0): [number, number][] | null {
  if (from === q.length) return []
  for (let i = w; i < f.starts.length; i++) {
    const s = f.starts[i]
    let n = 0
    while (from + n < q.length && f.text[s + n] === q[from + n]) n++
    // Try the longest run first, then shorter ones, so later words can take the rest.
    for (let k = n; k > 0; k--) {
      const rest = initials(f, q, from + k, i + 1)
      if (rest) return [span(f, s, k), ...rest]
    }
  }
  return null
}

/**
 * How well `text` matches a query: a prefix, then a word start, a substring,
 * every query word starting a word, or the query spelt by word beginnings.
 * Case and accents are ignored. Scattered letters do not match.
 */
export function match(text: string, query: string): Match | null {
  const q = foldQuery(query)
  if (!q) return null
  const f = fold(text)
  const tie = (pos: number) => -pos - f.text.length / 100
  if (f.text.startsWith(q)) return { score: PREFIX + tie(0), ranges: [span(f, 0, q.length)] }
  const word = f.starts.find((s) => f.text.startsWith(q, s))
  if (word !== undefined) return { score: WORD + tie(word), ranges: [span(f, word, q.length)] }
  const sub = f.text.indexOf(q)
  if (sub >= 0) return { score: SUBSTRING + tie(sub), ranges: [span(f, sub, q.length)] }
  if (q.includes(' ')) {
    const r = words(f, q)
    return r ? { score: WORDS + tie(r[0][0]), ranges: r } : null
  }
  const r = initials(f, q)
  return r && r.length > 1 ? { score: INITIALS + tie(r[0][0]), ranges: r } : null
}

/** The best of a title match and (ranked lower, without marks) a match on any secondary word. */
export function matchItem(title: string, keywords: string[], query: string): Match | null {
  const own = match(title, query)
  let best = own
  for (const k of keywords) {
    const m = match(k, query)
    if (m && (!best || m.score / 2 > best.score)) best = { score: m.score / 2, ranges: [] }
  }
  return best
}
