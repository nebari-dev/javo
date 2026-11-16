import { Home, Users } from 'lucide-react'
import { useEffect, useId, useRef, useState, type KeyboardEvent } from 'react'
import { Button } from '../../components/Button'
import { groupPlace, HOME_PLACE } from '../../lib/files'
import { useStorage } from '../../lib/queries'
import { HomeTree, TreeTools, useShowHidden, type TreeEntry } from './HomeTree'

const SAMPLE_TAG = 'Sample'
const SAMPLE_NEW_TAG = 'Sample, copied here when the app starts'
const SAMPLE_DIR_TAG = 'Made when the app starts'

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

/** No extensions means any file. */
function accepted(name: string, exts: string[]): boolean {
  const lower = name.toLowerCase()
  return exts.length === 0 || exts.some((e) => lower.endsWith(e.toLowerCase()))
}

/**
 * The dialog that chooses one file or folder of the user's home (the tree of
 * HomeTree) and, when choosing a file, looks through the folders of the
 * user's groups too. The template's sample opens with the folders on the way
 * to it open and it chosen, listed where it will be if it is not copied yet.
 */
export function HomePicker({
  extensions,
  acceptReason,
  folders = false,
  sample,
  onChoose,
  onClose,
}: {
  extensions: string[]
  acceptReason: string
  /** Choose a folder instead of a file. */
  folders?: boolean
  /** The template's sample (relative to the home): the picker opens where it is, with it selected. */
  sample?: string
  onChoose: (p: string) => void
  onClose: () => void
}) {
  const modal = useModalFocus(onClose)
  const placeId = useId()
  const title = folders ? 'Choose a folder' : 'Choose a file'
  const [place, setPlace] = useState(HOME_PLACE)
  const [query, setQuery] = useState('')
  const [showHidden, setShowHidden] = useShowHidden()
  const [selected, setSelected] = useState<string | null>(sample ?? null)
  const groupFolders = useStorage().data?.folders ?? []
  const inHome = place === HOME_PLACE
  const exts = inHome ? extensions : []
  const reason = inHome ? acceptReason : 'An app runs a file from your home folder'
  // With no folder chosen, folder mode chooses the home folder itself.
  const chosen = selected ?? (folders && inHome ? '' : null)

  const choosable = (e: TreeEntry) => (folders ? e.kind === 'dir' : e.kind === 'file' && accepted(e.name, exts))
  const tagOf = (e: TreeEntry) => {
    if (!inHome || !sample || (e.path !== sample && !sample.startsWith(`${e.path}/`))) return undefined
    if (e.path !== sample) return e.made ? SAMPLE_DIR_TAG : undefined
    return e.made ? SAMPLE_NEW_TAG : SAMPLE_TAG
  }
  // A place that is not ready says why (disabledReason, even "") and cannot be opened.
  const places: { id: string; label: string; disabledReason?: string }[] = [
    { id: HOME_PLACE, label: 'Home' },
    ...(folders ? [] : groupFolders.map((f) => ({ id: groupPlace(f.dir), label: f.dir, disabledReason: f.ready ? undefined : (f.reason?.title ?? '') }))),
  ]

  return (
    <div className="overlay open" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="picker" role="dialog" aria-modal="true" aria-label={title} ref={modal.dialog} onKeyDown={modal.onKeyDown}>
        <div className="pk-head">
          <h3>{title}</h3>
          <TreeTools query={query} onQuery={setQuery} showHidden={showHidden} onShowHidden={setShowHidden} searchRef={modal.search} />
        </div>
        <div className="pk-body">
          <div className="pk-side">
            <div className="nav-sec">Places</div>
            {places.map((pl, i) => {
              const off = pl.disabledReason !== undefined
              const why = `${placeId}-${i}`
              return (
                <div key={pl.id} className="pk-place">
                  <button
                    type="button"
                    className={pl.id === place ? 'on' : ''}
                    aria-current={pl.id === place ? 'true' : undefined}
                    aria-disabled={off ? 'true' : undefined}
                    aria-describedby={off ? why : undefined}
                    onClick={() => {
                      if (off) return
                      setSelected(null)
                      setPlace(pl.id)
                    }}
                  >
                    {pl.id === HOME_PLACE ? <Home size={16} aria-hidden="true" /> : <Users size={16} aria-hidden="true" />}
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
          <div className="pk-list">
            <HomeTree
              key={place}
              label={inHome ? 'Your home' : (places.find((p) => p.id === place)?.label ?? place)}
              place={place}
              query={query}
              showHidden={showHidden}
              meta
              expected={inHome && sample ? { path: sample, kind: folders ? 'dir' : 'file' } : undefined}
              openAt={sample ? [sample] : []}
              reveal={sample}
              look={(e) => {
                const ok = choosable(e)
                const tag = tagOf(e)
                // A folder in file mode opens rather than being chosen: no dimmed row.
                const dimmed = !ok && !(e.kind === 'dir' && !folders)
                return {
                  className: [selected === e.path && ok ? 'sel' : '', dimmed ? 'dim' : ''].filter(Boolean).join(' '),
                  note: tag && <span className="tree-tag">{tag}</span>,
                  disabled: !ok,
                  why: dimmed ? reason : undefined,
                  selected: ok ? selected === e.path : undefined,
                }
              }}
              onActivate={(e) => setSelected(e.path)}
            />
          </div>
        </div>
        <div className="pk-foot">
          <span className="pk-chosen muted">{chosen === null ? 'Nothing chosen yet' : chosen || 'Home folder'}</span>
          <span className="pk-actions">
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="primary" disabled={chosen === null} onClick={() => chosen !== null && onChoose(chosen)}>
              Choose
            </Button>
          </span>
        </div>
      </div>
    </div>
  )
}
