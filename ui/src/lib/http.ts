import { client } from '../api/client.gen'
import { noteIdpHeader } from './idp'

/** One request failure: the HTTP status (0 when the server was not reached) and the problem body. */
export class ApiError extends Error {
  readonly status: number
  readonly body: Problem
  readonly retryAfter: number | null

  constructor(status: number, body: unknown, headers?: Headers) {
    const b: Problem = typeof body === 'object' && body !== null ? (body as Problem) : {}
    super(b.detail || b.title || `HTTP ${status}`)
    this.status = status
    this.body = b
    const ra = Number(headers?.get('Retry-After'))
    this.retryAfter = Number.isFinite(ra) && ra > 0 ? ra : null
  }
}

/**
 * The gateway's error body: huma's problem fields, plus the reason catalog
 * fields some endpoints add (reason, what, todo, blame, who).
 */
export interface Problem {
  title?: string
  detail?: string
  status?: number
  errors?: { location?: string; message?: string }[] | null
  reason?: string
  what?: string
  todo?: string
  blame?: string
  who?: string
}

export function isUnauthorized(err: unknown): boolean {
  return err instanceof ApiError && err.status === 401
}

/** The text to show for a failed request: the server's own words. */
export function problemText(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 0) return 'Could not reach the server. Check your connection and try again.'
    return err.message
  }
  return String(err)
}

/**
 * Splits a 422 into messages per request field (location "body.<field>")
 * and messages that name no field.
 */
export function fieldErrors(err: unknown): { fields: Record<string, string>; other: string[] } {
  const fields: Record<string, string> = {}
  const other: string[] = []
  if (!(err instanceof ApiError)) return { fields, other: [problemText(err)] }
  for (const e of err.body.errors ?? []) {
    const f = e.location?.startsWith('body.') ? e.location.slice(5) : ''
    const msg = e.message ?? ''
    if (f && !fields[f]) fields[f] = msg
    else other.push(msg)
  }
  if (!err.body.errors?.length) other.push(problemText(err))
  return { fields, other }
}

// Every generated call throws ApiError, so callers and the session guard
// see one error type. baseUrl is set because Request needs an absolute URL
// outside a browser. X-Javo-Request marks a call as the UI's own: the
// gateway refuses a state-changing call with the session cookie without it,
// which another site cannot add.
client.setConfig({
  baseUrl: typeof window === 'undefined' ? '' : window.location.origin,
  throwOnError: true,
  headers: { 'X-Javo-Request': '1' },
})
client.interceptors.error.use((err, res) => (err instanceof ApiError ? err : new ApiError(res?.status ?? 0, err, res?.headers)))
client.interceptors.response.use((res) => {
  noteIdpHeader(res.headers)
  return res
})
