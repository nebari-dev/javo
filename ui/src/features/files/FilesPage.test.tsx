import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp, type Reply } from '../../test/api'

afterEach(() => vi.unstubAllGlobals())

const ME = { body: { id: 'alice-1', name: 'alice', displayName: 'Alice', roles: [], groups: [] } }

function listing(path: string, entries: { name: string; kind: 'file' | 'folder'; size?: number }[]): Reply {
  const full = entries.map((e) => ({ size: 0, modified: '2026-10-30T10:00:00Z', ...e }))
  return { body: { path, entries: full, count: full.length, truncated: false, stale: false, ageSeconds: 0 } }
}

describe('the file browser', () => {
  it('lists the home folder and opens folders and files', async () => {
    mockApi({
      'GET /api/me': ME,
      'GET /api/files': (c) =>
        c.path.includes('path=notes')
          ? listing('notes', [{ name: 'a.py', kind: 'file', size: 2048 }])
          : listing('', [
              { name: 'notes', kind: 'folder' },
              { name: '.bashrc', kind: 'file' },
              { name: 'README.md', kind: 'file', size: 12 },
            ]),
    })
    renderApp('/files')
    expect(await screen.findByRole('link', { name: /README\.md/ })).toBeTruthy()
    expect(screen.queryByRole('link', { name: /\.bashrc/ })).toBeNull()
    await userEvent.click(screen.getByRole('link', { name: /notes/ }))
    expect(location()).toBe('/files?path=notes')
    await userEvent.click(await screen.findByRole('link', { name: /a\.py/ }))
    // The file view loads on first visit; the route changes once it has.
    await waitFor(() => expect(location()).toBe('/files/view?path=notes%2Fa.py'))
  })

  it('creates an empty file and opens it', async () => {
    const api = mockApi({
      'GET /api/me': ME,
      'GET /api/files': listing('notes', []),
      'PUT /api/files/raw': { status: 201, body: { path: 'notes/new.md', size: 0, modified: '2026-11-03T12:00:00Z', etag: '"0-1"' } },
      'GET /api/files/raw': { body: '', headers: { ETag: '"0-1"', 'Content-Length': '0' } },
    })
    renderApp('/files?path=notes')
    await userEvent.click(await screen.findByRole('button', { name: 'New file' }))
    await userEvent.type(screen.getByRole('textbox', { name: 'File name' }), 'new.md{Enter}')
    await waitFor(() => expect(location()).toBe('/files/view?path=notes%2Fnew.md'))
    const put = api.calls.find((c) => c.method === 'PUT')!
    expect(put.path).toBe('/api/files/raw?path=notes%2Fnew.md')
    expect(put.body).toBeUndefined()
  })

  it('refuses a file name with a slash', async () => {
    const api = mockApi({ 'GET /api/me': ME, 'GET /api/files': listing('', []) })
    renderApp('/files')
    await userEvent.click(await screen.findByRole('button', { name: 'New file' }))
    await userEvent.type(screen.getByRole('textbox', { name: 'File name' }), 'a/b.md{Enter}')
    expect(await screen.findByText(/without a slash/)).toBeTruthy()
    expect(api.count('PUT', '/api/files/raw')).toBe(0)
  })
})
