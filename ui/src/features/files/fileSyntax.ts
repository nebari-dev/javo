// Syntax colours for the file viewer: Shiki with only the grammar of the
// file's language, each grammar its own lazily loaded chunk. Tokens carry
// CSS variables (--code-*), so the colours follow the app's light and dark
// tokens (pages.css). This module is loaded lazily by FileView.
import { createCssVariablesTheme, createHighlighterCore, type HighlighterCore, type LanguageInput } from '@shikijs/core'
import { createJavaScriptRegexEngine } from '@shikijs/engine-javascript'
import type { Language } from './fileKind'

export interface CodeToken {
  content: string
  color?: string
  italic?: boolean
}

const GRAMMARS: Record<Exclude<Language, 'plain'>, [string, LanguageInput]> = {
  python: ['python', () => import('@shikijs/langs/python')],
  markdown: ['markdown', () => import('@shikijs/langs/markdown')],
  json: ['json', () => import('@shikijs/langs/json')],
  yaml: ['yaml', () => import('@shikijs/langs/yaml')],
  javascript: ['javascript', () => import('@shikijs/langs/javascript')],
  jsx: ['jsx', () => import('@shikijs/langs/jsx')],
  typescript: ['typescript', () => import('@shikijs/langs/typescript')],
  tsx: ['tsx', () => import('@shikijs/langs/tsx')],
  html: ['html', () => import('@shikijs/langs/html')],
  css: ['css', () => import('@shikijs/langs/css')],
  sql: ['sql', () => import('@shikijs/langs/sql')],
  shell: ['shellscript', () => import('@shikijs/langs/shellscript')],
}

const theme = createCssVariablesTheme({ name: 'javo-code', variablePrefix: '--code-', fontStyle: true })

let highlighter: Promise<HighlighterCore> | null = null

/** Text longer than this is shown without colours: tokenizing it would hold the page up. */
export const MAX_HIGHLIGHT_CHARS = 300_000

/** The lines of text as coloured tokens, or null when the language has no grammar or the text is too long. */
export async function tokenize(text: string, language: Language): Promise<CodeToken[][] | null> {
  if (language === 'plain' || text.length > MAX_HIGHLIGHT_CHARS) return null
  const [name, grammar] = GRAMMARS[language]
  highlighter ??= createHighlighterCore({ themes: [theme], langs: [], engine: createJavaScriptRegexEngine() })
  const h = await highlighter
  if (!h.getLoadedLanguages().includes(name)) await h.loadLanguage(grammar)
  return h.codeToTokensBase(text, { lang: name, theme: 'javo-code' }).map((line) =>
    line.map((t) => ({ content: t.content, color: t.color, italic: ((t.fontStyle ?? 0) & 1) === 1 })),
  )
}
