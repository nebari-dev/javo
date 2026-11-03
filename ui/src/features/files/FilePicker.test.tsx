import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { FilePicker, type FilePickerProps } from './FilePicker'

function props(over: Partial<FilePickerProps> = {}): FilePickerProps {
  return {
    places: [
      { id: 'home', label: 'Home' },
      { id: 'shared', label: 'data-science' },
    ],
    activePlace: 'home',
    crumbs: [
      { label: 'Home', path: '~' },
      { label: 'sales', path: '~/sales' },
    ],
    entries: [
      { name: 'reports', path: '~/sales/reports', kind: 'dir', modified: 'Oct 28' },
      { name: 'app.py', path: '~/sales/app.py', kind: 'file', modified: 'Oct 29', size: '2 KB' },
      { name: 'notes.md', path: '~/sales/notes.md', kind: 'file', modified: 'Oct 20', size: '1 KB' },
    ],
    acceptExtensions: ['.py'],
    acceptReason: 'Streamlit runs .py files',
    showHidden: false,
    onShowHiddenChange: vi.fn(),
    onPlaceChange: vi.fn(),
    onNavigate: vi.fn(),
    onChoose: vi.fn(),
    onCancel: vi.fn(),
    ...over,
  }
}

describe('FilePicker', () => {
  it('lists places and breadcrumbs and reports navigation', async () => {
    const p = props()
    render(<FilePicker {...p} />)
    await userEvent.click(screen.getByRole('button', { name: 'data-science' }))
    expect(p.onPlaceChange).toHaveBeenCalledWith('shared')
    const nav = screen.getByRole('navigation', { name: 'Breadcrumbs' })
    await userEvent.click(within(nav).getByRole('button', { name: 'Home' }))
    expect(p.onNavigate).toHaveBeenCalledWith('~')
  })

  it('opens a folder when its row is clicked', async () => {
    const p = props()
    render(<FilePicker {...p} />)
    await userEvent.click(screen.getByRole('button', { name: /reports/ }))
    expect(p.onNavigate).toHaveBeenCalledWith('~/sales/reports')
  })

  it('accepts any file when no extensions are given', async () => {
    const p = props({ acceptExtensions: [] })
    render(<FilePicker {...p} />)
    const notes = screen.getByRole('button', { name: /notes\.md/ })
    expect(notes.getAttribute('aria-disabled')).toBeNull()
    await userEvent.click(notes)
    await userEvent.click(screen.getByRole('button', { name: 'Choose' }))
    expect(p.onChoose).toHaveBeenCalledWith('~/sales/notes.md')
  })

  it('disables files that do not match the accepted extensions and explains why', async () => {
    const p = props()
    render(<FilePicker {...p} />)
    const notes = screen.getByRole('button', { name: /notes\.md/ })
    expect(notes.getAttribute('aria-disabled')).toBe('true')
    expect(notes.getAttribute('title')).toBe('Streamlit runs .py files')
    await userEvent.click(notes)
    expect(screen.getByRole('button', { name: 'Choose' }).hasAttribute('disabled')).toBe(true)
    const app = screen.getByRole('button', { name: /app\.py/ })
    expect(app.getAttribute('aria-disabled')).toBeNull()
  })

  it('falls back to a generic reason built from the extensions', () => {
    render(<FilePicker {...props({ acceptReason: undefined })} />)
    expect(screen.getByRole('button', { name: /notes\.md/ }).getAttribute('title')).toBe('Only .py files can be chosen')
  })

  it('selects an accepted file and chooses it', async () => {
    const p = props()
    render(<FilePicker {...p} />)
    await userEvent.click(screen.getByRole('button', { name: /app\.py/ }))
    expect(screen.getByRole('button', { name: /app\.py/ }).getAttribute('aria-pressed')).toBe('true')
    await userEvent.click(screen.getByRole('button', { name: 'Choose' }))
    expect(p.onChoose).toHaveBeenCalledWith('~/sales/app.py')
  })

  it('cancels', async () => {
    const p = props()
    render(<FilePicker {...p} />)
    await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(p.onCancel).toHaveBeenCalledOnce()
  })

  it('filters rows by the search box', async () => {
    render(<FilePicker {...props()} />)
    await userEvent.type(screen.getByRole('searchbox', { name: 'Search this folder' }), 'rep')
    expect(screen.getByRole('button', { name: /reports/ })).toBeTruthy()
    expect(screen.queryByRole('button', { name: /app\.py/ })).toBeNull()
  })

  it('says so when nothing matches or the folder is empty', async () => {
    const { rerender } = render(<FilePicker {...props()} />)
    await userEvent.type(screen.getByRole('searchbox'), 'zzz')
    expect(screen.getByText('No files match your search')).toBeTruthy()
    rerender(<FilePicker {...props({ entries: [] })} />)
    expect(screen.getByText('No files match your search')).toBeTruthy()
  })

  it('shows an empty folder message', () => {
    render(<FilePicker {...props({ entries: [] })} />)
    expect(screen.getByText('This folder is empty')).toBeTruthy()
  })

  it('shows an error with a retry and no rows', async () => {
    const onRetry = vi.fn()
    render(<FilePicker {...props({ error: 'Could not read your home folder', onRetry })} />)
    expect(screen.getByRole('alert').textContent).toContain('Could not read your home folder')
    expect(screen.queryByRole('button', { name: /app\.py/ })).toBeNull()
    await userEvent.click(screen.getByRole('button', { name: 'Retry' }))
    expect(onRetry).toHaveBeenCalledOnce()
  })
})

describe('FilePicker notice', () => {
  it('shows a notice in place of the rows', () => {
    render(<FilePicker {...props({ notice: <p>Opening your home folder</p> })} />)
    expect(screen.getByText('Opening your home folder')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /app\.py/ })).toBeNull()
  })

  it('disables a place that is not ready and gives its reason as text', async () => {
    const p = props({
      places: [
        { id: 'home', label: 'Home' },
        { id: 'group:ml', label: 'ml', disabledReason: 'Group folders are not available' },
      ],
    })
    render(<FilePicker {...p} />)
    const place = screen.getByRole('button', { name: /^ml/ })
    expect(place.getAttribute('aria-disabled')).toBe('true')
    expect(document.getElementById(place.getAttribute('aria-describedby') ?? '')?.textContent).toBe('Group folders are not available')
    await userEvent.click(place)
    expect(p.onPlaceChange).not.toHaveBeenCalled()
  })
})

describe('FilePicker folder mode', () => {
  const folders = (over: Partial<FilePickerProps> = {}) => props({ choose: 'folder', acceptReason: 'Claude Code opens a folder', ...over })

  it('selects a folder by its row and opens it with its own button', async () => {
    const p = folders()
    render(<FilePicker {...p} />)
    expect(screen.getByRole('dialog', { name: 'Choose a folder' })).toBeTruthy()
    const reports = screen.getByRole('button', { name: /^reports/ })
    await userEvent.click(reports)
    expect(reports.getAttribute('aria-pressed')).toBe('true')
    expect(p.onNavigate).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: 'Choose' }))
    expect(p.onChoose).toHaveBeenCalledWith('~/sales/reports')
    await userEvent.click(screen.getByRole('button', { name: 'Open reports' }))
    expect(p.onNavigate).toHaveBeenCalledWith('~/sales/reports')
  })

  it('greys out files and explains why', () => {
    render(<FilePicker {...folders()} />)
    const app = screen.getByRole('button', { name: /app\.py/ })
    expect(app.getAttribute('aria-disabled')).toBe('true')
    expect(app.getAttribute('title')).toBe('Claude Code opens a folder')
  })

  it('chooses the folder it stands in when no row is selected', async () => {
    const p = folders()
    render(<FilePicker {...p} />)
    expect(screen.getByText('~/sales')).toBeTruthy()
    const choose = screen.getByRole('button', { name: 'Choose' })
    expect(choose.hasAttribute('disabled')).toBe(false)
    await userEvent.click(choose)
    expect(p.onChoose).toHaveBeenCalledWith('~/sales')
  })

  it('names the home folder when it stands in it', () => {
    render(<FilePicker {...folders({ crumbs: [{ label: 'Home', path: '' }] })} />)
    expect(screen.getByText('Home folder')).toBeTruthy()
  })

  it('drops the selection when it opens another folder', async () => {
    const p = folders()
    render(<FilePicker {...p} />)
    await userEvent.click(screen.getByRole('button', { name: /^reports/ }))
    await userEvent.click(screen.getByRole('button', { name: 'Open reports' }))
    expect(screen.getByRole('button', { name: /^reports/ }).getAttribute('aria-pressed')).toBe('false')
  })
})

describe('FilePicker starting selection', () => {
  it('starts with the given entry selected, scrolled into view and labelled', async () => {
    const scroll = vi.fn()
    Element.prototype.scrollIntoView = scroll
    const p = props({
      initialSelected: '~/sales/app.py',
      entries: [{ name: 'app.py', path: '~/sales/app.py', kind: 'file', tag: 'Sample, copied here when the app starts' }],
    })
    render(<FilePicker {...p} />)
    const app = screen.getByRole('button', { name: /app\.py/ })
    expect(app.getAttribute('aria-pressed')).toBe('true')
    expect(app.textContent).toContain('Sample, copied here when the app starts')
    expect(scroll).toHaveBeenCalled()
    expect(scroll.mock.contexts[0]).toBe(app)
    await userEvent.click(screen.getByRole('button', { name: 'Choose' }))
    expect(p.onChoose).toHaveBeenCalledWith('~/sales/app.py')
  })
})

describe('FilePicker hidden entries', () => {
  const dotted = [
    { name: '.git', path: '~/.git', kind: 'dir' as const },
    { name: '.env', path: '~/.env', kind: 'file' as const },
    { name: 'app.py', path: '~/app.py', kind: 'file' as const },
  ]

  it('hides names starting with a dot until asked to show them', async () => {
    const p = props({ entries: dotted })
    const { rerender } = render(<FilePicker {...p} />)
    expect(screen.queryByRole('button', { name: /\.git/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /\.env/ })).toBeNull()
    const show = screen.getByRole('switch', { name: 'Show hidden files' })
    expect(show.getAttribute('aria-checked')).toBe('false')
    // The words are on screen, not only in a tooltip.
    expect(screen.getByText('Show hidden files')).toBeTruthy()
    await userEvent.click(show)
    expect(p.onShowHiddenChange).toHaveBeenCalledWith(true)
    rerender(<FilePicker {...p} showHidden />)
    expect(screen.getByRole('button', { name: /\.git/ })).toBeTruthy()
    expect(screen.getByRole('button', { name: /\.env/ })).toBeTruthy()
    expect(screen.getByRole('switch', { name: 'Show hidden files' }).getAttribute('aria-checked')).toBe('true')
  })
})
