// Log line syntax: Shiki's `log` grammar (VS Code's Log File Highlighter
// grammar) tokenizes the common shapes; the small `javo-log` grammar below
// runs first for shapes it does not know (klog, Jupyter, logfmt and JSON
// levels, HTTP access lines, Python and Go stack frames, file:line).
// This module is loaded lazily by LogLines.
import { createHighlighterCoreSync } from '@shikijs/core'
import { createJavaScriptRegexEngine } from '@shikijs/engine-javascript'
import logLang from '@shikijs/langs/log'
import type { LanguageRegistration, ThemeRegistration } from '@shikijs/core'

export type LogLevel = 'error' | 'warn' | 'info' | 'debug'

export type TokenKind =
  | LogLevel
  | 'time'
  | 'source'
  | 'string'
  | 'number'
  | 'constant'
  | 'url'
  | 'path'
  | 'key'
  | 'method'
  | 's2xx'
  | 's3xx'
  | 's4xx'
  | 's5xx'
  | 'exception'
  | 'frame'
  | 'name'

export interface LogToken {
  text: string
  kind?: TokenKind
}

export interface LineSyntax {
  level?: LogLevel
  tokens: LogToken[]
}

const LEVELS: readonly TokenKind[] = ['error', 'warn', 'info', 'debug']

const METHODS = 'GET|HEAD|POST|PUT|PATCH|DELETE|OPTIONS|CONNECT|TRACE'

// Level words per level, as logfmt and JSON values (case-insensitive).
const LEVEL_WORDS: [LogLevel, string][] = [
  ['error', 'error|err|eror|fatal|panic|crit|critical|alert|emerg|emergency'],
  ['warn', 'warn|warning'],
  ['info', 'info|notice'],
  ['debug', 'debug|trace|verbose'],
]

// Bracketed words Shiki's grammar already reads as a level, so `[error]` is not a component.
const BRACKET_LEVELS = 'v|vb|vrb|verb|verbose|d|de|dbg|dbug|debug|i|in|inf|info|information|w|wn|wrn|warn|warning|e|er|err|eror|error|f|fa|ftl|fatl|fatal'

const CODE_EXT = 'go|py|pyx|ipynb|R|r|jl|rs|js|mjs|cjs|ts|tsx|jsx|java|kt|scala|c|cc|cpp|cxx|h|hpp|rb|php|cs|swift|sh'

const s = (kind: TokenKind) => `javo.${kind}`

const statusClasses = [2, 3, 4, 5].map((c) => [c, `s${c}xx` as TokenKind] as const)

const javoLog: LanguageRegistration = {
  name: 'javo-log',
  scopeName: 'text.javo-log',
  embeddedLangs: ['log'],
  repository: {},
  patterns: [
    // klog: Lmmdd hh:mm:ss.uuuuuu threadid file:line] msg
    ...(
      [
        ['I', 'info'],
        ['W', 'warn'],
        ['E', 'error'],
        ['F', 'error'],
      ] as const
    ).map(([letter, level]) => ({
      match: `^(${letter})(\\d{4} \\d{2}:\\d{2}:\\d{2}\\.\\d{6})(\\s+\\d+ )([\\w./-]+:\\d+)(\\])`,
      captures: { 1: { name: s(level) }, 2: { name: s('time') }, 4: { name: s('path') } },
    })),
    // Jupyter (tornado LogFormatter): [L yyyy-mm-dd hh:mm:ss.mmm Name] msg
    ...(
      [
        ['D', 'debug'],
        ['I', 'info'],
        ['W', 'warn'],
        ['E', 'error'],
        ['C', 'error'],
      ] as const
    ).map(([letter, level]) => ({
      match: `^(\\[)(${letter})( )(\\d{4}-\\d{2}-\\d{2} \\d{2}:\\d{2}:\\d{2}(?:\\.\\d+)?)( )([\\w.-]+)(\\])`,
      captures: { 2: { name: s(level) }, 4: { name: s('time') }, 6: { name: s('source') } },
    })),
    { match: '^Traceback(?= \\(most recent call last\\):)', name: s('error') },
    { match: '^(?:panic|fatal error)(?=: )', name: s('error') },
    // Go panic frames.
    { match: '^goroutine \\d+ \\[[^\\]]*\\]:$', name: s('frame') },
    { match: '^(?:[\\w.-]+/)*[\\w.-]+\\.(?:\\(\\*?\\w+\\)\\.)?[\\w.]+\\(.*\\)$', name: s('frame') },
    {
      match: '^(\\t)((?:/|[A-Za-z]:\\\\)?[\\w./\\\\@+-]+\\.go:\\d+)( \\+0x[0-9a-f]+)?$',
      captures: { 1: { name: s('frame') }, 2: { name: s('path') }, 3: { name: s('frame') } },
    },
    // Python frame: File "path", line N, in name
    {
      match: '^(\\s+File )("[^"]+")(, line )(\\d+)(?:(, in )(.+))?$',
      captures: {
        1: { name: s('frame') },
        2: { name: s('path') },
        3: { name: s('frame') },
        4: { name: s('number') },
        5: { name: s('frame') },
        6: { name: s('frame') },
      },
    },
    // An exception that starts the line ends a traceback: KeyError: 'region'.
    { match: '^[A-Za-z_][\\w.]*(?:Error|Exception)(?=:|$)', name: s('exception') },
    { match: '\\b[A-Z]\\w*(?:Error|Exception)\\b', name: s('exception') },
    { match: '\\b[A-Z]\\w+Warning\\b', name: s('warn') },
    // Deprecation notices written as sentences, with no level word (Streamlit's
    // default format, many libraries' print warnings).
    { match: '(?i)\\b(?:deprecated|deprecation|will be removed|no longer supported)\\b', name: s('warn') },
    // Timestamps.
    { match: '\\b(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) [ \\d]\\d \\d{2}:\\d{2}:\\d{2}\\b', name: s('time') },
    {
      match: '\\b\\d{4}[-/]\\d{2}[-/]\\d{2}(?:[T ]\\d{2}:\\d{2}:\\d{2}(?:[.,]\\d+)?(?:Z|[+-]\\d{2}:?\\d{2})?)?(?![\\w.])',
      name: s('time'),
    },
    // HTTP: "METHOD path HTTP/x" status, then status METHOD path, then METHOD path.
    ...statusClasses.map(([c, kind]) => ({
      match: `(")(${METHODS})( )(\\S+)( HTTP/[\\d.]+")( )(${c}\\d\\d)\\b`,
      captures: { 2: { name: s('method') }, 4: { name: s('path') }, 7: { name: s(kind) } },
    })),
    ...statusClasses.map(([c, kind]) => ({
      match: `(?<![\\w.])(${c}\\d\\d)( )(${METHODS})( )(\\S+)`,
      captures: { 1: { name: s(kind) }, 3: { name: s('method') }, 5: { name: s('path') } },
    })),
    { match: `\\b(${METHODS})( )(/\\S*)`, captures: { 1: { name: s('method') }, 3: { name: s('path') } } },
    ...statusClasses.map(([c, kind]) => ({
      match: `(?:\\b(status|status_code|code)(=)|("(?:status|status_code|code)")(\\s*:\\s*))(${c}\\d\\d)\\b`,
      captures: { 1: { name: s('key') }, 3: { name: s('key') }, 5: { name: s(kind) } },
    })),
    // Levels as logfmt and JSON values.
    ...LEVEL_WORDS.flatMap(([level, words]) => [
      {
        match: `\\b(level|lvl|severity)(=)("?(?i:${words})"?)(?![\\w])`,
        captures: { 1: { name: s('key') }, 3: { name: s(level) } },
      },
      {
        match: `("(?:level|lvl|severity)")(\\s*:\\s*)("(?i:${words})")`,
        captures: { 1: { name: s('key') }, 3: { name: s(level) } },
      },
    ]),
    { match: '(?<=[{,]|[{,] )"(?:[^"\\\\]|\\\\.)*"(?=\\s*:)', name: s('key') },
    { match: '"(?:[^"\\\\]|\\\\.)*"', name: s('string') },
    {
      match: `\\b[a-z][a-z0-9+.-]*://[^\\s"'<>()\\[\\]{}]*[^\\s"'<>()\\[\\]{},.;:!?]`,
      name: s('url'),
    },
    { match: '\\b([A-Za-z_][\\w.-]*)(=)', captures: { 1: { name: s('key') } } },
    { match: '\\b[A-Za-z_]\\w*(?:::~?[A-Za-z_]\\w*)+', name: s('name') },
    { match: `(?<![\\w/.])(?:\\.{0,2}/)?(?:[\\w.@+-]+/)*[\\w.@+-]+\\.(?:${CODE_EXT}):\\d+(?::\\d+)?\\b`, name: s('path') },
    { match: '(?<![\\w/.:~])/[\\w.@+~-]+(?:/[\\w.@+~-]*)*(?::\\d+(?::\\d+)?)?', name: s('path') },
    {
      match: `(\\[)((?!(?i:${BRACKET_LEVELS})\\])[A-Za-z][\\w.:/-]*)(\\])`,
      captures: { 2: { name: s('source') } },
    },
    { match: '(?<![\\w.])\\d{1,3}(?:\\.\\d{1,3}){3}(?::\\d+)?(?![\\w.])', name: s('number') },
    // Version numbers (3.9.2, 1.27.0) are one number, not three.
    { match: '(?<![\\w.])v?\\d+(?:\\.\\d+){2,}(?![\\w.])', name: s('number') },
    { match: '(?<![\\w.])-?\\d+(?:\\.\\d+)?(?:ms|us|µs|ns|s|m|h|[KMGT]i?B|B|%)?(?![\\w.])', name: s('number') },
    { include: 'text.log' },
  ],
}

// The "colors" here are token kinds; Shiki hands them back as each token's color.
const kindRule = (kind: TokenKind, scope: string[]) => ({ scope, settings: { foreground: kind } })
const KIND_RULES = [
  kindRule('error', ['log.error', s('error')]),
    kindRule('warn', ['log.warning', s('warn')]),
    kindRule('info', ['log.info', s('info')]),
    kindRule('debug', ['log.debug', 'log.verbose', s('debug')]),
    kindRule('time', ['log.date', s('time')]),
    kindRule('string', ['log.string', s('string')]),
    kindRule('constant', ['log.constant']),
    kindRule('exception', ['log.exceptiontype', s('exception')]),
    kindRule('frame', ['log.exception', s('frame')]),
    ...(['source', 'number', 'url', 'path', 'key', 'method', 's2xx', 's3xx', 's4xx', 's5xx', 'name'] as const).map((k) => kindRule(k, [s(k)])),
]
const theme: ThemeRegistration = { name: 'javo-log-kinds', type: 'light', fg: 'plain', bg: 'none', settings: [...KIND_RULES] }
// Read before Shiki adds its own default rule to the theme.
const KINDS = new Set<string>(KIND_RULES.map((r) => r.settings.foreground))

const highlighter = createHighlighterCoreSync({
  themes: [theme],
  langs: [logLang, javoLog],
  engine: createJavaScriptRegexEngine(),
})

function levelOf(tokens: LogToken[]): LogLevel | undefined {
  const first = tokens.find((t) => t.kind && LEVELS.includes(t.kind))
  if (first) return first.kind as LogLevel
  return tokens[0]?.kind === 'exception' ? 'error' : undefined
}

// Lines repeat (replays, reruns), and old lines are seen on every render.
const cache = new Map<string, LineSyntax>()
const CACHE_MAX = 5000

/** The kinded pieces of one log line (plain text, no ANSI codes) and its level. */
export function highlightLine(text: string): LineSyntax {
  const hit = cache.get(text)
  if (hit) return hit
  const tokens: LogToken[] = []
  for (const t of highlighter.codeToTokensBase(text, { lang: 'javo-log', theme: 'javo-log-kinds' })[0] ?? []) {
    const kind = t.color && KINDS.has(t.color) ? (t.color as TokenKind) : undefined
    const last = tokens[tokens.length - 1]
    if (last && last.kind === kind) last.text += t.content
    else tokens.push(kind ? { text: t.content, kind } : { text: t.content })
  }
  const result = { level: levelOf(tokens), tokens }
  if (cache.size >= CACHE_MAX) cache.delete(cache.keys().next().value!)
  cache.set(text, result)
  return result
}
