import { saveRawFile, type SavedFile } from '../../api'
import { ApiError } from '../../lib/http'
import { rawFileUrl } from '../../lib/files'

/** The largest file the viewer shows as text and the editor saves (the gateway's cap). */
export const MAX_TEXT_BYTES = 2 << 20

/**
 * One read of a file for the viewer: its text and version (the ETag a save
 * sends back), or why it is not shown as text.
 */
export type FileRead = { kind: 'text'; text: string; etag: string } | { kind: 'binary' } | { kind: 'large'; size: number }

/** Text is UTF-8 without NUL bytes, the same test the gateway applies to a save. */
function asText(bytes: ArrayBuffer): string | null {
  try {
    const text = new TextDecoder('utf-8', { fatal: true }).decode(bytes)
    return text.includes('\0') ? null : text
  } catch {
    return null
  }
}

/** Reads a file of the home; one over MAX_TEXT_BYTES is not downloaded. */
export async function readHomeFile(path: string): Promise<FileRead> {
  const ctl = new AbortController()
  const res = await fetch(rawFileUrl(path), { signal: ctl.signal, credentials: 'same-origin', cache: 'no-store' })
  if (!res.ok) {
    const body = await res.json().catch(() => ({}))
    throw new ApiError(res.status, body, res.headers)
  }
  const size = Number(res.headers.get('Content-Length'))
  if (size > MAX_TEXT_BYTES) {
    ctl.abort()
    return { kind: 'large', size }
  }
  const text = asText(await res.arrayBuffer())
  return text === null ? { kind: 'binary' } : { kind: 'text', text, etag: res.headers.get('ETag') ?? '' }
}

/**
 * Saves text as the file: over the version etag names, or as a new file when
 * etag is null. A file changed since, or a name taken, is an ApiError 409.
 */
export async function saveHomeFile(path: string, text: string, etag: string | null): Promise<SavedFile> {
  const headers = etag === null ? { 'If-None-Match': '*' } : { 'If-Match': etag }
  const { data } = await saveRawFile({
    query: { path },
    body: new Blob([text], { type: 'text/plain' }),
    headers,
    throwOnError: true,
  })
  return data
}
