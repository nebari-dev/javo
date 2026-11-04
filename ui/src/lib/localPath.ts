/**
 * The path to go to after sign-in, or "/" when next is not a path on this
 * host. The server's rule (httpx.IsLocalPath): one leading slash, not two, no
 * backslash, tab, CR or LF (browsers drop those or read them as a slash, so
 * they can name another host), and it must resolve to this origin.
 */
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || /[\\\t\r\n]/.test(next)) return '/'
  try {
    const base = 'https://javo.invalid'
    if (new URL(next, base).origin !== base) return '/'
  } catch {
    return '/'
  }
  return next
}
