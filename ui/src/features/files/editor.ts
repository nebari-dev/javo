// The file editor: CodeMirror 6, loaded lazily by FileView so the main
// bundle does not carry it, with each language's support in a chunk of its
// own. CodeMirror injects its styles; in a document that means a <style>
// element, which the UI's Content Security Policy (style-src 'self') blocks,
// so the editor lives in a shadow root, where its styles go into a
// constructed style sheet instead. The app's CSS variables (colours, fonts)
// reach inside it.
import { defaultKeymap, history, historyKeymap, indentWithTab } from '@codemirror/commands'
import { bracketMatching, HighlightStyle, indentOnInput, StreamLanguage, syntaxHighlighting, type LanguageSupport } from '@codemirror/language'
import { EditorState, type Extension } from '@codemirror/state'
import { drawSelection, EditorView, highlightActiveLine, highlightActiveLineGutter, keymap, lineNumbers } from '@codemirror/view'
import { tags as t } from '@lezer/highlight'
import type { Language } from './fileKind'

export interface EditorHandle {
  text(): string
  focus(): void
  destroy(): void
}

export interface EditorOptions {
  doc: string
  language: Language
  /** Called on every change with whether the text differs from doc. */
  onChange: (dirty: boolean) => void
  /** Ctrl+S or Cmd+S. */
  onSave: () => void
}

const LANGUAGES: Record<Exclude<Language, 'plain'>, () => Promise<LanguageSupport | Extension>> = {
  python: async () => (await import('@codemirror/lang-python')).python(),
  markdown: async () => (await import('@codemirror/lang-markdown')).markdown(),
  json: async () => (await import('@codemirror/lang-json')).json(),
  yaml: async () => (await import('@codemirror/lang-yaml')).yaml(),
  javascript: async () => (await import('@codemirror/lang-javascript')).javascript(),
  jsx: async () => (await import('@codemirror/lang-javascript')).javascript({ jsx: true }),
  typescript: async () => (await import('@codemirror/lang-javascript')).javascript({ typescript: true }),
  tsx: async () => (await import('@codemirror/lang-javascript')).javascript({ jsx: true, typescript: true }),
  html: async () => (await import('@codemirror/lang-html')).html(),
  css: async () => (await import('@codemirror/lang-css')).css(),
  sql: async () => (await import('@codemirror/lang-sql')).sql(),
  shell: async () => StreamLanguage.define((await import('@codemirror/legacy-modes/mode/shell')).shell),
}

// The same --code-* colours the viewer's Shiki tokens use (pages.css).
const highlight = HighlightStyle.define([
  { tag: [t.keyword, t.modifier, t.operatorKeyword, t.controlKeyword, t.definitionKeyword, t.moduleKeyword], color: 'var(--code-token-keyword)' },
  { tag: [t.string, t.special(t.string), t.regexp, t.character], color: 'var(--code-token-string)' },
  { tag: [t.number, t.bool, t.null, t.atom, t.constant(t.name), t.standard(t.name)], color: 'var(--code-token-constant)' },
  { tag: [t.comment, t.lineComment, t.blockComment, t.docComment], color: 'var(--code-token-comment)', fontStyle: 'italic' },
  { tag: [t.function(t.variableName), t.function(t.propertyName), t.className, t.typeName, t.tagName, t.heading], color: 'var(--code-token-function)' },
  { tag: [t.propertyName, t.attributeName, t.labelName], color: 'var(--code-token-parameter)' },
  { tag: [t.punctuation, t.bracket, t.operator, t.separator], color: 'var(--code-token-punctuation)' },
  { tag: [t.link, t.url], color: 'var(--code-token-link)', textDecoration: 'underline' },
  { tag: t.heading, fontWeight: '600' },
  { tag: t.emphasis, fontStyle: 'italic' },
  { tag: t.strong, fontWeight: '600' },
  { tag: t.invalid, color: 'var(--err)' },
])

const theme = EditorView.theme({
  '&': { color: 'var(--code-foreground)', backgroundColor: 'var(--log-bg)', fontSize: 'var(--fs-xs)', height: '100%' },
  '&.cm-focused': { outline: 'none' },
  '.cm-scroller': { fontFamily: 'var(--font-mono)', lineHeight: '1.6' },
  '.cm-content': { caretColor: 'var(--text)', padding: '10px 0' },
  '.cm-cursor, .cm-dropCursor': { borderLeftColor: 'var(--text)' },
  '&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground, .cm-selectionBackground, ::selection': {
    backgroundColor: 'var(--accent-soft)',
  },
  '.cm-gutters': { backgroundColor: 'var(--log-head)', color: 'var(--text-3)', border: 'none', borderRight: '1px solid var(--border)' },
  '.cm-lineNumbers .cm-gutterElement': { padding: '0 12px 0 16px' },
  '.cm-activeLine': { backgroundColor: 'color-mix(in srgb, var(--accent-soft) 45%, transparent)' },
  '.cm-activeLineGutter': { backgroundColor: 'var(--accent-soft)', color: 'var(--text-2)' },
  '.cm-matchingBracket, &.cm-focused .cm-matchingBracket': { backgroundColor: 'var(--accent-soft)', outline: '1px solid var(--border-strong)' },
})

/** Builds the editor in parent (a shadow root is made inside it) and returns a handle to it. */
export async function createEditor(parent: HTMLElement, o: EditorOptions): Promise<EditorHandle> {
  const lang = o.language === 'plain' ? [] : await LANGUAGES[o.language]()
  const shadow = parent.shadowRoot ?? parent.attachShadow({ mode: 'open' })
  const host = document.createElement('div')
  host.style.height = '100%'
  shadow.appendChild(host)
  const initial = EditorState.create({ doc: o.doc }).doc
  const view = new EditorView({
    root: shadow,
    parent: host,
    state: EditorState.create({
      doc: o.doc,
      extensions: [
        lineNumbers(),
        highlightActiveLineGutter(),
        highlightActiveLine(),
        drawSelection(),
        history(),
        indentOnInput(),
        bracketMatching(),
        syntaxHighlighting(highlight),
        keymap.of([
          { key: 'Mod-s', preventDefault: true, run: () => (o.onSave(), true) },
          ...defaultKeymap,
          ...historyKeymap,
          indentWithTab,
        ]),
        EditorView.contentAttributes.of({ 'aria-label': 'File text' }),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) o.onChange(!u.state.doc.eq(initial))
        }),
        theme,
        lang,
      ],
    }),
  })
  return {
    text: () => view.state.doc.toString(),
    focus: () => view.focus(),
    destroy: () => {
      view.destroy()
      host.remove()
    },
  }
}
