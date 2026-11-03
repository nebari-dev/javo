/** How the file viewer shows a file: in an img, in a frame, or read as text (if it is text). */
export type FileKind = 'image' | 'pdf' | 'text'

/** The languages the viewer highlights and the editor knows; plain is none. */
export type Language =
  | 'python'
  | 'markdown'
  | 'json'
  | 'yaml'
  | 'javascript'
  | 'jsx'
  | 'typescript'
  | 'tsx'
  | 'html'
  | 'css'
  | 'sql'
  | 'shell'
  | 'plain'

const IMAGES = new Set(['png', 'jpg', 'jpeg', 'gif', 'webp', 'svg'])

const BY_EXTENSION: Record<string, Language> = {
  py: 'python',
  md: 'markdown',
  markdown: 'markdown',
  json: 'json',
  ipynb: 'json',
  yml: 'yaml',
  yaml: 'yaml',
  js: 'javascript',
  mjs: 'javascript',
  cjs: 'javascript',
  jsx: 'jsx',
  ts: 'typescript',
  mts: 'typescript',
  tsx: 'tsx',
  html: 'html',
  htm: 'html',
  css: 'css',
  sql: 'sql',
  sh: 'shell',
  bash: 'shell',
  zsh: 'shell',
}

const BY_NAME: Record<string, Language> = { '.bashrc': 'shell', '.profile': 'shell', '.bash_profile': 'shell', '.zshrc': 'shell' }

function extension(name: string): string {
  const i = name.lastIndexOf('.')
  return i <= 0 ? '' : name.slice(i + 1).toLowerCase()
}

export function fileKind(name: string): FileKind {
  const ext = extension(name)
  if (IMAGES.has(ext)) return 'image'
  return ext === 'pdf' ? 'pdf' : 'text'
}

export function languageOf(name: string): Language {
  return BY_NAME[name] ?? BY_EXTENSION[extension(name)] ?? 'plain'
}
