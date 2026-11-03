import { QueryClientProvider } from '@tanstack/react-query'
import { render } from '@testing-library/react'
import type { ReactNode } from 'react'
import { MemoryRouter, useLocation } from 'react-router'
import { vi } from 'vitest'
import { AppRoutes } from '../app/App'
import { makeQueryClient } from '../lib/queries'

export interface Reply {
  status?: number
  body?: unknown
  headers?: Record<string, string>
  /** The response waits for this promise, so a test can keep a request in flight. */
  hold?: Promise<unknown>
}

export interface Call {
  method: string
  path: string
  /** Parsed JSON, or the text of a body that is not JSON (an uploaded file). */
  body: unknown
  contentType: string | null
}

type Handler = Reply | ((call: Call) => Reply)

const WARM = 'POST /api/files/warm'

/**
 * Replaces fetch with a table keyed by "METHOD /path" (query string ignored
 * in the key). An array answers in order and repeats its last entry.
 * Unmatched requests answer 500 so a missing mock fails loudly.
 */
export function mockApi(routes: Record<string, Handler | Handler[]>) {
  const calls: Call[] = []
  const seen: Record<string, number> = {}
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), window.location.origin), init)
    const url = new URL(req.url)
    const text = req.method === 'GET' ? '' : await req.text()
    const contentType = req.headers.get('Content-Type')
    const isJSON = !contentType || contentType.includes('json')
    const call: Call = {
      method: req.method,
      path: url.pathname + url.search,
      body: text ? (isJSON ? JSON.parse(text) : text) : undefined,
      contentType,
    }
    const key = `${req.method} ${url.pathname}`
    // Pages warm the home folder on load; a test that does not mock it
    // neither sees the call nor has to answer it.
    if (key === WARM && !(key in routes)) return json({ status: 204 })
    calls.push(call)
    const entry = routes[key]
    if (!entry) return json({ status: 500, body: { title: `unmocked ${key}` } })
    let h: Handler
    if (Array.isArray(entry)) {
      const i = seen[key] ?? 0
      seen[key] = i + 1
      h = entry[Math.min(i, entry.length - 1)]
    } else h = entry
    const reply = typeof h === 'function' ? h(call) : h
    if (reply.hold) await reply.hold
    return json(reply)
  })
  vi.stubGlobal('fetch', fetchMock)
  return { calls, count: (method: string, path: string) => calls.filter((c) => c.method === method && c.path.split('?')[0] === path).length }
}

function json(r: Reply): Response {
  const status = r.status ?? 200
  // Text, or text that keeps coming (a followed log), as plain text.
  if (typeof r.body === 'string' || r.body instanceof ReadableStream) {
    return new Response(r.body, { status, headers: { 'Content-Type': 'text/plain; charset=utf-8', ...r.headers } })
  }
  const body = status === 204 ? null : JSON.stringify(r.body ?? {})
  const type = status >= 400 ? 'application/problem+json' : 'application/json'
  return new Response(body, { status, headers: { 'Content-Type': type, ...r.headers } })
}

function LocationProbe() {
  const loc = useLocation()
  return <output data-testid="location">{loc.pathname + loc.search}</output>
}

export function renderApp(path: string) {
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter initialEntries={[path]}>
        <AppRoutes />
        <LocationProbe />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

export function location(): string {
  return document.querySelector('[data-testid="location"]')?.textContent ?? ''
}

/** Renders one component with the data and routing it needs, without the app shell. */
export function renderIn(ui: ReactNode) {
  return render(
    <QueryClientProvider client={makeQueryClient()}>
      <MemoryRouter>{ui}</MemoryRouter>
    </QueryClientProvider>,
  )
}
