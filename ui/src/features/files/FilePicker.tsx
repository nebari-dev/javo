import { ChevronRight, File, Folder, Home, LoaderCircle, Search, Users } from 'lucide-react'
import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { EmptyState } from '../../components/EmptyState'
import { Switch } from '../../components/Switch'

interface PickerPlace {
  id: string
  label: string
  /** Set (even to "") when the place cannot be opened; shown under it as text. */
  disabledReason?: string
}

interface PickerCrumb {
  label: string
  path: string
}

export interface PickerEntry {
  name: string
  path: string
  kind: 'dir' | 'file'
  modified?: string
  size?: string
  /** A short marker shown on the row, e.g. that this is the template's sample. */
  tag?: string
}

/** What a caller adds to make the picker a dialog of its own: its name, what cannot be chosen, and what choosing does. */
export interface PickerExtras {
  /** The dialog's name in place of "Choose a file", and a line under it. */
  title?: string
  subtitle?: string
  /** Why the entry at this path cannot be chosen, or undefined when it can; its row says why, in red when it never can. */
  refused?: (path: string) => { why: string; never?: boolean } | undefined
  /** The label of the button that chooses path (null: nothing to choose yet); default Choose. */
  chooseLabel?: (path: string | null) => string
  /** More to decide with the choice, between the list and the buttons. */
  options?: ReactNode
  /** The choice is being saved: the button waits. */
  busy?: boolean
  /** Why saving the choice failed. */
  failure?: string
}

export interface FilePickerProps extends PickerExtras {
  places: PickerPlace[]
  activePlace: string
  crumbs: PickerCrumb[]
  entries: PickerEntry[]
  /** Lowercase-insensitive suffixes such as ".py". Files with other suffixes are disabled. */
  acceptExtensions: string[]
  /** Tooltip for disabled files, e.g. "Streamlit runs .py files". */
  acceptReason?: string
  /** 'folder' chooses folders: a folder row selects it, its own button opens it, files are greyed out. 'any' is that with files chosen too, and nothing chosen until a row is selected. */
  choose?: 'file' | 'folder' | 'any'
  /** The entry selected when the picker opens; it is scrolled into view. */
  initialSelected?: string
  /** Whether names starting with a dot are listed. */
  showHidden: boolean
  onShowHiddenChange: (show: boolean) => void
  error?: string
  /** The folder has not been read yet: placeholder rows stand in for it. */
  loading?: boolean
  /** The rows are the last listing seen and a fresh one is on its way. */
  updating?: boolean
  /** Shown instead of the rows (above the placeholder rows while loading), e.g. why the folder is taking long. */
  notice?: ReactNode
  /** A quiet line under the rows, e.g. that the listing was cut short. */
  note?: ReactNode
  onRetry?: () => void
  onPlaceChange: (id: string) => void
  onNavigate: (path: string) => void
  onChoose: (path: string) => void
  onCancel: () => void
}

/** No extensions means any file. */
function accepted(name: string, exts: string[]): boolean {
  const lower = name.toLowerCase()
  return exts.length === 0 || exts.some((e) => lower.endsWith(e.toLowerCase()))
}

/** Name widths of the placeholder rows, varied so they read as a list. */
const SKELETON_NAMES = ['42%', '58%', '34%', '50%', '38%', '46%']

function SkeletonRows() {
  return (
    <div className="pk-skeleton" role="status" aria-label="Loading this folder">
      {SKELETON_NAMES.map((w) => (
        <div key={w} className="pk-row sk" aria-hidden="true">
          <span className="fi">
            <span className="sk-bar sk-icon" />
          </span>
          <span className="sk-bar" style={{ width: w }} />
          <span className="md">
            <span className="sk-bar sk-meta" />
          </span>
          <span className="sz">
            <span className="sk-bar sk-meta" />
          </span>
        </div>
      ))}
    </div>
  )
}

const FOCUSABLE = 'button:not([disabled]), input:not([disabled]), [href], [tabindex]:not([tabindex="-1"])'

/**
 * Modal keyboard behaviour: focus starts in the search field, Tab and
 * Shift+Tab cycle inside the dialog, Escape cancels, and focus goes back to
 * whatever opened the dialog when it closes.
 */
function useModalFocus(onCancel: () => void) {
  const dialog = useRef<HTMLDivElement>(null)
  const search = useRef<HTMLInputElement>(null)
  const cancel = useRef(onCancel)
  cancel.current = onCancel

  useEffect(() => {
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null
    search.current?.focus()
    return () => {
      if (opener?.isConnected) opener.focus()
    }
  }, [])

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if (e.code === 'Escape' || e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      cancel.current()
      return
    }
    if (e.key !== 'Tab' || !dialog.current) return
    const items = Array.from(dialog.current.querySelectorAll<HTMLElement>(FOCUSABLE))
    if (items.length === 0) return
    const first = items[0]
    const last = items[items.length - 1]
    const at = document.activeElement
    if (e.shiftKey && (at === first || !dialog.current.contains(at))) {
      e.preventDefault()
      last.focus()
    } else if (!e.shiftKey && (at === last || !dialog.current.contains(at))) {
      e.preventDefault()
      first.focus()
    }
  }
  return { dialog, search, onKeyDown }
}

export function FilePicker(p: FilePickerProps) {
  const modal = useModalFocus(p.onCancel)
  const folders = p.choose === 'folder' || p.choose === 'any'
  const title = p.title ?? (folders ? 'Choose a folder' : 'Choose a file')
  const placeId = useId()
  const [query, setQuery] = useState('')
  const [selected, setSelected] = useState<string | null>(p.initialSelected ?? null)
  const reason = p.acceptReason ?? `Only ${p.acceptExtensions.join(' or ')} files can be chosen`
  const q = query.trim().toLowerCase()
  const shown = p.showHidden ? p.entries : p.entries.filter((e) => !e.name.startsWith('.'))
  const rows = q ? shown.filter((e) => e.name.toLowerCase().includes(q)) : shown
  // With no folder selected, folder mode chooses the folder it stands in;
  // 'any' chooses only what was selected.
  const here = p.crumbs[p.crumbs.length - 1]?.path ?? ''
  const chosen = selected ?? (p.choose === 'folder' && !p.error ? here : null)
  const chosenRefused = chosen === null ? undefined : p.refused?.(chosen)
  const choosable = chosen !== null && chosenRefused === undefined

  // Bring the starting selection into view once its row is listed.
  const start = useRef<HTMLButtonElement | null>(null)
  const scrolled = useRef(false)
  useEffect(() => {
    if (scrolled.current || !start.current) return
    scrolled.current = true
    start.current.scrollIntoView?.({ block: 'nearest' })
  })
  const startRef = (path: string) => (path === p.initialSelected ? start : undefined)

  // A folder selection belongs to the folder it was made in.
  const navigate = (path: string) => {
    if (folders) setSelected(null)
    p.onNavigate(path)
  }
  const tag = (e: PickerEntry) => e.tag && <span className="pk-tag">{e.tag}</span>

  return (
    <div className="picker" role="dialog" aria-modal="true" aria-label={title} ref={modal.dialog} onKeyDown={modal.onKeyDown}>
      <div className="pk-head">
        <div className="pk-title">
          <h3>{title}</h3>
          {p.subtitle && <p className="tight muted small">{p.subtitle}</p>}
        </div>
        {p.updating && (
          <span className="pk-updating" role="status" aria-label="Updating this folder">
            <LoaderCircle size={14} aria-hidden="true" />
            Updating
          </span>
        )}
        <label className="pk-search">
          <Search size="1em" aria-hidden="true" />
          <input
            ref={modal.search}
            type="search"
            aria-label="Search this folder"
            placeholder="Search this folder"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </label>
        <Switch label="Show hidden files" checked={p.showHidden} onChange={p.onShowHiddenChange} />
      </div>
      <div className="pk-body">
        <div className="pk-side">
          <div className="nav-sec">Places</div>
          {p.places.map((pl, i) => {
            const off = pl.disabledReason !== undefined
            const why = `${placeId}-${i}`
            return (
              <div key={pl.id} className="pk-place">
                <button
                  type="button"
                  className={pl.id === p.activePlace ? 'on' : ''}
                  aria-current={pl.id === p.activePlace ? 'true' : undefined}
                  aria-disabled={off ? 'true' : undefined}
                  aria-describedby={off ? why : undefined}
                  onClick={() => {
                    if (off) return
                    if (folders) setSelected(null)
                    p.onPlaceChange(pl.id)
                  }}
                >
                  {pl.id === 'home' ? <Home size={16} aria-hidden="true" /> : <Users size={16} aria-hidden="true" />}
                  {pl.label}
                </button>
                {off && (
                  <span id={why} className="pk-why">
                    {pl.disabledReason}
                  </span>
                )}
              </div>
            )
          })}
        </div>
        <div className="pk-main">
          <nav className="pk-crumbs" aria-label="Breadcrumbs">
            {p.crumbs.map((c, i) => (
              <span key={c.path} className="pk-crumb">
                {i > 0 && <ChevronRight size={14} aria-hidden="true" className="faint" />}
                <button type="button" onClick={() => navigate(c.path)}>
                  {c.label}
                </button>
              </span>
            ))}
          </nav>
          <div className="pk-list">
            {p.error ? (
              <Banner
                tone="err"
                title={p.error}
                actions={
                  p.onRetry && (
                    <Button size="sm" onClick={p.onRetry}>
                      Retry
                    </Button>
                  )
                }
              />
            ) : p.loading ? (
              <>
                {p.notice}
                <SkeletonRows />
              </>
            ) : p.notice ? (
              p.notice
            ) : rows.length === 0 ? (
              q ? (
                <EmptyState title="No files match your search" body="Try a shorter or different name." />
              ) : (
                <EmptyState title="This folder is empty" body="Pick another folder from the list on the left." />
              )
            ) : (
              rows.map((e) => {
                if (e.kind === 'dir' && !folders) {
                  return (
                    <button key={e.path} ref={startRef(e.path)} type="button" className="pk-row" onClick={() => navigate(e.path)}>
                      <span className="fi">
                        <Folder size={18} aria-hidden="true" />
                      </span>
                      <span className="nm dir">
                        {e.name}
                        {tag(e)}
                      </span>
                      <span className="md">{e.modified}</span>
                      <span className="sz" aria-hidden="true">
                        <ChevronRight size={14} />
                      </span>
                    </button>
                  )
                }
                const dir = e.kind === 'dir'
                const no = p.refused?.(e.path)
                const ok = no === undefined && (p.choose === 'any' || (folders ? dir : accepted(e.name, p.acceptExtensions)))
                const sel = ok && selected === e.path
                const row = (
                  <button
                    key={e.path}
                    ref={startRef(e.path)}
                    type="button"
                    className={`pk-row${ok ? '' : no?.never ? ' no' : ' dim'}${sel ? ' sel' : ''}`}
                    aria-disabled={ok ? undefined : 'true'}
                    aria-pressed={ok ? sel : undefined}
                    title={ok ? undefined : (no?.why ?? reason)}
                    onClick={() => ok && setSelected(e.path)}
                    onDoubleClick={() => dir && navigate(e.path)}
                  >
                    <span className="fi">{dir ? <Folder size={18} aria-hidden="true" /> : <File size={18} aria-hidden="true" />}</span>
                    <span className={`nm${dir ? ' dir' : ''}`}>
                      {e.name}
                      {tag(e)}
                      {no !== undefined && <span className={no.never ? 'pk-tag no' : 'pk-tag off'}>{no.why}</span>}
                    </span>
                    <span className="md">{e.modified}</span>
                    <span className="sz">{e.size}</span>
                  </button>
                )
                if (!folders) return row
                return (
                  <div key={e.path} className="pk-item">
                    {row}
                    {dir ? (
                      <Button size="sm" variant="ghost" icon={ChevronRight} aria-label={`Open ${e.name}`} title={`Open ${e.name}`} onClick={() => navigate(e.path)} />
                    ) : (
                      <span aria-hidden="true" />
                    )}
                  </div>
                )
              })
            )}
            {p.note && !p.error && !p.notice && !p.loading && <div className="pk-note faint">{p.note}</div>}
          </div>
        </div>
      </div>
      {p.options && <div className="pk-options">{p.options}</div>}
      <div className="pk-foot">
        {p.failure ? (
          <span className="pk-failure errmsg" role="alert">
            {p.failure}
          </span>
        ) : (
          // A button that names the choice says it already.
          !p.chooseLabel && <span className="pk-chosen muted">{chosen === null ? 'Nothing chosen yet' : chosen || 'Home folder'}</span>
        )}
        <span className="pk-actions">
          <Button onClick={p.onCancel}>Cancel</Button>
          <Button variant="primary" disabled={!choosable || p.busy} onClick={() => choosable && p.onChoose(chosen)}>
            {p.chooseLabel ? p.chooseLabel(choosable ? chosen : null) : 'Choose'}
          </Button>
        </span>
      </div>
    </div>
  )
}
