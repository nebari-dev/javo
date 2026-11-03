import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { location, mockApi, renderApp, type Call } from '../../test/api'

// CodeMirror needs a real layout; the editor is a textarea here, behind the
// same small interface the page uses.
vi.mock('./editor', () => ({
  createEditor: async (parent: HTMLElement, o: { doc: string; onChange: (dirty: boolean) => void; onSave: () => void }) => {
    const ta = document.createElement('textarea')
    ta.setAttribute('aria-label', 'File text')
    ta.value = o.doc
    ta.addEventListener('input', () => o.onChange(ta.value !== o.doc))
    ta.addEventListener('keydown', (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault()
        o.onSave()
      }
    })
    parent.appendChild(ta)
    return { text: () => ta.value, focus: () => ta.focus(), destroy: () => ta.remove() }
  },
}))

afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

const ME = { body: { id: 'alice-1', name: 'alice', displayName: 'Alice', roles: [], groups: [] } }

function text(body: string, etag = '"9-1"') {
  return { body, headers: { ETag: etag, 'Content-Length': String(new TextEncoder().encode(body).length) } }
}

function saved(size: number, etag: string) {
  return { body: { path: 'notes/a.py', size, modified: '2026-11-03T12:00:00Z', etag }, headers: { ETag: etag } }
}

const openEditor = async () => {
  await userEvent.click(await screen.findByRole('button', { name: 'Edit' }))
  return (await screen.findByRole('textbox', { name: 'File text' })) as HTMLTextAreaElement
}

describe('the file view', () => {
  it('shows a text file read-only with line numbers', async () => {
    mockApi({ 'GET /api/me': ME, 'GET /api/files/raw': text('import os\nprint(os.getcwd())\n') })
    renderApp('/files/view?path=notes/a.py')
    expect(await screen.findByRole('heading', { name: 'a.py' })).toBeTruthy()
    const code = await screen.findByRole('region', { name: 'Contents of a.py' })
    expect(code.textContent).toContain('print(os.getcwd())')
    expect([...code.querySelectorAll('.fv-ln')].map((n) => n.textContent)).toEqual(['1', '2'])
    expect(screen.queryByRole('textbox')).toBeNull()
  })

  it('saves an edit with the version it read, and shows the saved text', async () => {
    const api = mockApi({
      'GET /api/me': ME,
      'GET /api/files/raw': text('print(1)\n'),
      'PUT /api/files/raw': saved(9, '"9-2"'),
    })
    renderApp('/files/view?path=notes/a.py')
    const editor = await openEditor()
    fireEvent.input(editor, { target: { value: 'print(2)\n' } })
    fireEvent.keyDown(editor, { key: 's', ctrlKey: true })
    await waitFor(() => expect(api.count('PUT', '/api/files/raw')).toBe(1))
    const put = api.calls.find((c: Call) => c.method === 'PUT')!
    expect(put.path).toBe('/api/files/raw?path=notes%2Fa.py')
    expect(put.body).toBe('print(2)\n')
    expect(await screen.findByRole('region', { name: 'Contents of a.py' })).toBeTruthy()
    expect(screen.getByRole('region', { name: 'Contents of a.py' }).textContent).toContain('print(2)')
    expect(screen.getByText('Saved', { selector: '[role=status]' })).toBeTruthy()
  })

  it('sends If-Match with the ETag the read carried', async () => {
    const seen: (string | null)[] = []
    mockApi({ 'GET /api/me': ME, 'GET /api/files/raw': text('a\n', '"2-7"'), 'PUT /api/files/raw': saved(2, '"2-8"') })
    const mocked = globalThis.fetch
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      if (input instanceof Request && input.method === 'PUT') seen.push(input.headers.get('If-Match'))
      return mocked(input, init)
    })
    renderApp('/files/view?path=notes/a.py')
    const editor = await openEditor()
    fireEvent.input(editor, { target: { value: 'b\n' } })
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(seen).toEqual(['"2-7"']))
  })

  it('on a conflict keeps the edit and offers to load the new version', async () => {
    const api = mockApi({
      'GET /api/me': ME,
      'GET /api/files/raw': [text('one\n'), text('changed elsewhere\n', '"18-3"')],
      'PUT /api/files/raw': {
        status: 409,
        body: { status: 409, title: 'This file changed since you opened it. Reload it to see the new version, then make your edit again.' },
      },
    })
    renderApp('/files/view?path=notes/a.py')
    const editor = await openEditor()
    fireEvent.input(editor, { target: { value: 'mine\n' } })
    await userEvent.click(screen.getByRole('button', { name: 'Save' }))
    expect(await screen.findByText(/changed since you opened it/)).toBeTruthy()
    expect((screen.getByRole('textbox', { name: 'File text' }) as HTMLTextAreaElement).value).toBe('mine\n')
    vi.spyOn(window, 'confirm').mockReturnValue(true)
    await userEvent.click(screen.getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(api.count('GET', '/api/files/raw')).toBe(2))
    expect((await screen.findByRole('region', { name: 'Contents of a.py' })).textContent).toContain('changed elsewhere')
  })

  it('asks before throwing away unsaved changes', async () => {
    mockApi({ 'GET /api/me': ME, 'GET /api/files/raw': text('one\n') })
    renderApp('/files/view?path=notes/a.py')
    const editor = await openEditor()
    fireEvent.input(editor, { target: { value: 'two\n' } })
    const ask = vi.spyOn(window, 'confirm').mockReturnValue(false)
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(ask).toHaveBeenCalledOnce()
    expect(screen.getByRole('textbox', { name: 'File text' })).toBeTruthy()
    const leave = new Event('beforeunload', { cancelable: true })
    act(() => {
      window.dispatchEvent(leave)
    })
    expect(leave.defaultPrevented).toBe(true)
    ask.mockReturnValue(true)
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect((await screen.findByRole('region', { name: 'Contents of a.py' })).textContent).toContain('one')
  })

  it('shows a picture in the page, without reading it as text', async () => {
    const api = mockApi({ 'GET /api/me': ME })
    renderApp('/files/view?path=plots/fig%201.png')
    const img = (await screen.findByRole('img', { name: 'fig 1.png' })) as HTMLImageElement
    expect(img.getAttribute('src')).toBe('/api/files/raw?path=plots%2Ffig%201.png')
    expect(api.count('GET', '/api/files/raw')).toBe(0)
    expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull()
  })

  it('offers a download for a file that is not text or too large to show', async () => {
    mockApi({
      'GET /api/me': ME,
      'GET /api/files/raw': [
        { body: 'PAR1\0\0', headers: { 'Content-Length': '6' } },
        { body: 'x', headers: { 'Content-Length': String(3 << 20) } },
      ],
    })
    const { unmount } = renderApp('/files/view?path=data.parquet')
    expect(await screen.findByText(/cannot be shown here/)).toBeTruthy()
    expect(screen.getByRole('link', { name: /Download/ }).getAttribute('href')).toBe('/api/files/raw?path=data.parquet')
    expect(screen.queryByRole('button', { name: 'Edit' })).toBeNull()
    unmount()
    renderApp('/files/view?path=big.log')
    expect(await screen.findByText(/too large to show here/)).toBeTruthy()
  })

  it('goes up to the folder from its breadcrumbs', async () => {
    mockApi({ 'GET /api/me': ME, 'GET /api/files/raw': text('x\n'), 'GET /api/files': { body: { path: 'notes', entries: [], count: 0, truncated: false, stale: false, ageSeconds: 0 } } })
    renderApp('/files/view?path=notes/a.py')
    await userEvent.click(await screen.findByRole('link', { name: 'notes' }))
    expect(location()).toBe('/files?path=notes')
  })
})
