import { memo, useEffect, useState, type CSSProperties, type ReactNode } from 'react'
import { ansiRuns, plainText, type AnsiRun } from './ansi'
import type { LineSyntax, LogLevel, LogToken } from './logSyntax'

export interface LogLine {
  /** A stable key, so a list that drops old lines from the top does not redraw every row. */
  id?: number | string
  /** ISO time the line was written or the event happened. */
  time?: string
  /** A short tag before the text, such as an event reason. */
  label?: string
  text: string
  warn?: boolean
}

type Syntax = typeof import('./logSyntax')

// The highlighter (Shiki and its grammars) is its own chunk, fetched as soon as
// this module loads; until it arrives lines show as plain text.
let syntax: Syntax | null = null
const loading: Promise<Syntax | null> = import('./logSyntax').then(
  (m) => (syntax = m),
  () => null,
)

function useSyntax(): Syntax | null {
  const [s, setS] = useState(syntax)
  useEffect(() => {
    if (!s) void loading.then(setS)
  }, [s])
  return s
}

function clock(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour12: false })
}

const SPOKEN: Partial<Record<LogLevel, string>> = { error: 'Error', warn: 'Warning' }

// Lines that carry on the one before: indented, stack frames, "Caused by", Go's signal line.
const CONTINUES = /^(?:\s|Caused by: |\[signal |goroutine \d+ \[)/

function continues(text: string, syn: LineSyntax | undefined): boolean {
  return CONTINUES.test(text) || syn?.tokens[0]?.kind === 'frame'
}

interface Row {
  line: LogLine
  plain: string
  ansi: AnsiRun[] | null
  syn?: LineSyntax
  level?: LogLevel
  /** The line has a level of its own (not one carried from the line it belongs to). */
  own: boolean
}

/**
 * Each line's level. A stack trace or other continuation under an error or
 * warning keeps that level, so the whole block reads as one; a blank line
 * does too when the block goes on after it.
 */
function rows(lines: LogLine[], s: Syntax | null): Row[] {
  const out: Row[] = lines.map((line) => {
    const ansi = ansiRuns(line.text)
    const plain = ansi ? ansi.map((r) => r.text).join('') : plainText(line.text)
    const syn = s?.highlightLine(plain)
    const level = syn?.level ?? (line.warn ? 'warn' : undefined)
    return { line, plain, ansi, syn, level, own: level !== undefined }
  })
  let carried: LogLevel | undefined
  out.forEach((r, i) => {
    if (r.own) {
      carried = r.level === 'error' || r.level === 'warn' ? r.level : undefined
      return
    }
    if (!carried) return
    const next = out[i + 1]
    const goesOn = r.plain.trim() === '' ? !!next && !next.own && continues(next.plain, next.syn) : continues(r.plain, r.syn)
    if (goesOn) r.level = carried
    else carried = undefined
  })
  return out
}

function tokenSpans(tokens: LogToken[], from: number, to: number, key: string): ReactNode[] {
  const out: ReactNode[] = []
  let at = 0
  tokens.forEach((t, i) => {
    const start = Math.max(at, from)
    const end = Math.min(at + t.text.length, to)
    if (start < end) {
      const text = t.text.slice(start - at, end - at)
      out.push(t.kind ? <span key={`${key}.${i}`} className={`lk-${t.kind}`}>{text}</span> : text)
    }
    at += t.text.length
  })
  return out
}

/** The line's text: the app's own ANSI colours win; everything else gets syntax colours. */
function content({ plain, ansi, syn }: Row): ReactNode {
  if (!ansi) return syn ? tokenSpans(syn.tokens, 0, plain.length, 't') : plain
  let at = 0
  return ansi.map((r, i) => {
    const from = at
    at += r.text.length
    if (r.className || r.style) {
      return (
        <span key={i} className={r.className} style={r.style}>
          {r.text}
        </span>
      )
    }
    return syn ? tokenSpans(syn.tokens, from, at, String(i)) : r.text
  })
}

const LogRow = memo(function LogRow({ row }: { row: Row }) {
  const { line, level, own } = row
  const spoken = own && level ? SPOKEN[level] : undefined
  return (
    <li data-level={level}>
      {line.time && (
        <time className="log-time" dateTime={line.time}>
          {clock(line.time)}
        </time>
      )}
      <span className="log-line">
        {line.label && <span className="log-label">{line.label}</span>}
        {spoken && <span className="sr-only">{spoken}</span>}
        <span className="log-text">{content(row)}</span>
      </span>
    </li>
  )
}, sameRow)

function sameRow(a: { row: Row }, b: { row: Row }): boolean {
  return a.row.line === b.row.line && a.row.syn === b.row.syn && a.row.level === b.row.level && a.row.own === b.row.own
}

/**
 * A log, highlighted: each line's level tints it (errors red, warnings amber,
 * debug dimmed) and its parts (times, components, strings, numbers, paths,
 * URLs, keys, HTTP methods and statuses, exceptions, stack frames) are coloured.
 * Every line is untrusted text and rendered as text only. The gutter shows
 * each line's time, or its number when the log has no times (drawn by CSS,
 * so it is not part of the line's text).
 */
export function LogLines({ lines, label }: { lines: LogLine[]; label: string }) {
  const s = useSyntax()
  const numbered = !lines.some((l) => l.time)
  return (
    <ol
      className={`log${numbered ? ' numbered' : ''}`}
      aria-label={label}
      style={numbered ? ({ '--log-gutter': `${String(lines.length).length + 1}ch` } as CSSProperties) : undefined}
    >
      {rows(lines, s).map((r, i) => (
        <LogRow key={r.line.id ?? i} row={r} />
      ))}
    </ol>
  )
}
