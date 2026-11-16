import { ChevronRight, File, Folder, LoaderCircle, Search } from 'lucide-react'
import { createContext, useContext, useEffect, useRef, useState, type CSSProperties, type KeyboardEvent, type ReactNode } from 'react'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { EmptyState } from '../../components/EmptyState'
import { Switch } from '../../components/Switch'
import { formatSize, HOME_PLACE, type HomeEntry } from '../../lib/files'
import { ApiError, problemText } from '../../lib/http'
import { useHomeFolder, useMe } from '../../lib/queries'
import { readStored, writeStored } from '../../lib/storage'
import { FailureCard } from '../logs/FailureCard'

/** One folder or file of a place, as a row of the tree. */
export interface TreeEntry {
  name: string
  /** Relative to the place. */
  path: string
  kind: 'dir' | 'file'
  modified?: string
  size?: string
  /** Not there yet: listed where it will be (see HomeTree's expected). */
  made?: boolean
}

/** What a row adds to the shared one: its look and what it says and holds besides its name. */
export interface RowLook {
  /** Classes on the row, for its look: sel (chosen), dim, no, or a caller's own. */
  className?: string
  /** Before the icon, such as a tick box. */
  lead?: ReactNode
  /** Words after the name. */
  note?: ReactNode
  /** At the end of the line, such as a switch or how open it is. */
  trail?: ReactNode
  /** Under the line, such as a question about this row. */
  below?: ReactNode
  /** False for a folder whose contents are not to be shown. */
  expands?: boolean
  /** Whether activating the row (a click, Space or Enter) does something; when not, it says why (title). */
  disabled?: boolean
  why?: string
  selected?: boolean
}

interface Tree {
  place: string
  query: string
  showHidden: boolean
  meta: boolean
  keep?: (path: string) => boolean
  expected?: { path: string; kind: 'dir' | 'file' }
  look: (e: TreeEntry) => RowLook
  onActivate: (e: TreeEntry, toggle: () => void) => void
  isOpen: (path: string) => boolean
  toggle: (path: string) => void
  /** The row arrow keys and Tab start from. */
  active: string | null
  setActive: (path: string) => void
  first: string | null
  /** The row brought into view once listed. */
  reveal?: string
}

const TreeContext = createContext<Tree | null>(null)

const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name)

/** True when path is dir or inside it. */
const within = (dir: string, path: string) => path === dir || path.startsWith(`${dir}/`)

function formatDate(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' })
}

function toEntry(dir: string, e: HomeEntry): TreeEntry {
  const dirKind = e.kind === 'folder'
  return { name: e.name, path: join(dir, e.name), kind: dirKind ? 'dir' : 'file', modified: formatDate(e.modified), size: dirKind ? undefined : formatSize(e.size) }
}

/**
 * The listing of dir with the step towards the expected path in it: that
 * path itself or the folder that leads to it. What is expected may not be
 * there yet (a sample copied when an app first starts, an agent's own project
 * made when it starts), so a step that is missing is listed as where it will
 * be, among the entries of its kind.
 */
function withExpected(dir: string, entries: TreeEntry[], expected: { path: string; kind: 'dir' | 'file' } | undefined): TreeEntry[] {
  if (!expected || (dir && !expected.path.startsWith(`${dir}/`))) return entries
  const name = expected.path.slice(dir ? dir.length + 1 : 0).split('/')[0]
  if (entries.some((e) => e.name === name)) return entries
  const path = join(dir, name)
  const kind = path === expected.path ? expected.kind : 'dir'
  const step: TreeEntry = { name, path, kind, made: true }
  const at = entries.findIndex((e) => (e.kind === kind && e.name.localeCompare(name) > 0) || (kind === 'dir' && e.kind === 'file'))
  return at < 0 ? [...entries, step] : [...entries.slice(0, at), step, ...entries.slice(at)]
}

/** How long a folder may take to open before the tree says why it is waiting. */
const SLOW_MS = 3000

/** True once active has held for ms with the same key; a new key starts the wait again. */
function useAfter(active: boolean, ms: number, key: string): boolean {
  const [doneFor, setDoneFor] = useState<string | null>(null)
  useEffect(() => {
    if (!active) return
    const t = setTimeout(() => setDoneFor(key), ms)
    return () => clearTimeout(t)
  }, [active, ms, key])
  return active && doneFor === key
}

/** Name widths of the placeholder rows, varied so they read as a list. */
const SKELETON_NAMES = ['42%', '58%', '34%', '50%', '38%', '46%']

function SkeletonRows() {
  return (
    <div className="tree-skeleton" role="status" aria-label="Loading this folder">
      {SKELETON_NAMES.map((w) => (
        <div key={w} className="tree-line sk" aria-hidden="true">
          <span className="sk-bar sk-icon" />
          <span className="sk-bar" style={{ width: w }} />
        </div>
      ))}
    </div>
  )
}

const hiddenKey = (user: string) => `javo.picker.hidden.${user}`

/** Whether the person lists names that start with a dot; remembered for them, off until they turn it on. */
export function useShowHidden(): [boolean, (show: boolean) => void] {
  const user = useMe().data?.id ?? ''
  const [show, setShow] = useState(() => readStored(hiddenKey(user)) === 'show')
  useEffect(() => {
    setShow(readStored(hiddenKey(user)) === 'show')
  }, [user])
  return [
    show,
    (on) => {
      setShow(on)
      writeStored(hiddenKey(user), on ? 'show' : 'hide')
    },
  ]
}

/** The search box and the hidden-files switch every tree of the home is worked with. */
export function TreeTools({ query, onQuery, showHidden, onShowHidden, searchRef }: {
  query: string
  onQuery: (q: string) => void
  showHidden: boolean
  onShowHidden: (show: boolean) => void
  searchRef?: React.Ref<HTMLInputElement>
}) {
  return (
    <>
      <label className="tree-search">
        <Search size="1em" aria-hidden="true" />
        <input ref={searchRef} type="search" aria-label="Search by name" placeholder="Search by name" value={query} onChange={(e) => onQuery(e.target.value)} />
      </label>
      <Switch label="Show hidden files and folders" checked={showHidden} onChange={onShowHidden} />
    </>
  )
}

/** One folder or file: the shared line (expander, icon, name, date and size), with what the caller adds. */
function EntryRow({ e, level }: { e: TreeEntry; level: number }) {
  const t = useContext(TreeContext)!
  const look = t.look(e)
  const opens = e.kind === 'dir' && look.expands !== false
  const open = opens && t.isOpen(e.path)
  const ref = useRef<HTMLDivElement>(null)
  const revealed = useRef(false)
  useEffect(() => {
    if (revealed.current || t.reveal !== e.path) return
    revealed.current = true
    ref.current?.scrollIntoView?.({ block: 'nearest' })
  })
  const Icon = e.kind === 'dir' ? Folder : File
  return (
    <>
      <div
        ref={ref}
        role="treeitem"
        aria-label={e.name}
        aria-level={level}
        aria-expanded={opens ? open : undefined}
        aria-selected={look.selected === undefined ? undefined : look.selected}
        aria-disabled={look.disabled || undefined}
        title={look.disabled ? look.why : undefined}
        tabIndex={t.active === e.path || (t.active === null && t.first === e.path) ? 0 : -1}
        className={['tree-row', look.className].filter(Boolean).join(' ')}
        style={{ '--tree-level': level - 1 } as CSSProperties}
        onFocus={(ev) => ev.target === ev.currentTarget && t.setActive(e.path)}
        onClick={(ev) => {
          // Controls inside the row do their own thing.
          if ((ev.target as HTMLElement).closest('button, input, a, [role=radiogroup]')) return
          if (!look.disabled) t.onActivate(e, () => t.toggle(e.path))
          else if (opens) t.toggle(e.path)
        }}
        onDoubleClick={() => opens && !look.disabled && t.toggle(e.path)}
      >
        <div className="tree-line">
          {opens ? (
            <button type="button" className="tree-twist" tabIndex={-1} aria-label={`${open ? 'Collapse' : 'Expand'} ${e.name}`} onClick={() => t.toggle(e.path)}>
              <ChevronRight size={16} aria-hidden="true" />
            </button>
          ) : (
            <span className="tree-twist" />
          )}
          {look.lead}
          <Icon size={18} aria-hidden="true" className="tree-icon" />
          <span className="tree-name">
            <span className="mono">{e.name}</span>
            {look.note && <span className="tree-note">{look.note}</span>}
          </span>
          {t.meta && (
            <span className="tree-meta">
              <span>{e.modified}</span>
              <span className="mono">{e.size}</span>
            </span>
          )}
          {look.trail}
        </div>
        {look.below}
      </div>
      {open && <FolderRows dir={e.path} level={level + 1} />}
    </>
  )
}

/**
 * What is in one folder, read when it is first shown, with the API's own
 * words while it waits or when it fails. A folder on the way to what is
 * expected that is not there yet is listed as holding just that step.
 */
function FolderRows({ dir, level }: { dir: string; level: number }) {
  const t = useContext(TreeContext)!
  const q = useHomeFolder(dir, true, t.place)
  const top = level === 1
  const towards = !!t.expected && (dir === '' || within(dir, t.expected.path))
  const missing = towards && q.error instanceof ApiError && q.error.status === 404
  const err = missing ? null : q.error
  const read = q.data
  const listing = read?.kind === 'listed' ? read.folder : undefined
  const loading = !listing && !err && !missing
  const slow = useAfter(loading, SLOW_MS, `${t.place}\n${dir}`)
  const style = { '--tree-level': level - 1 } as CSSProperties
  const note = (body: ReactNode, cls = '') => (
    <div className={`tree-msg${cls}`} style={style}>
      {body}
    </div>
  )

  if (err) {
    const reason = err instanceof ApiError && err.body.reason ? err.body : undefined
    if (reason && top) return <FailureCard title={reason.title} what={reason.what} todo={reason.todo} blame={reason.blame} who={reason.who} />
    return note(
      <Banner
        tone="err"
        title={problemText(err)}
        actions={
          <Button size="sm" onClick={() => void q.refetch()}>
            Retry
          </Button>
        }
      />,
    )
  }
  if (loading) {
    const opening = read?.kind === 'opening' ? read.reason : undefined
    return (
      <>
        {slow && opening && (
          <Banner tone="info" title={opening.title}>
            <p className="tight">{opening.what}</p>
            <p className="tight">{opening.todo}</p>
          </Banner>
        )}
        {top ? <SkeletonRows /> : note(<span role="status">Loading</span>, ' faint')}
      </>
    )
  }

  const listed = (listing?.entries ?? []).map((e) => toEntry(dir, e))
  const q2 = t.query.trim().toLowerCase()
  const rows = withExpected(dir, listed, t.place === HOME_PLACE ? t.expected : undefined).filter(
    (e) =>
      (t.showHidden || !e.name.startsWith('.') || !!t.keep?.(e.path)) &&
      // A search keeps what matches, and open folders, so what is in them can match.
      (!q2 || e.name.toLowerCase().includes(q2) || (e.kind === 'dir' && t.isOpen(e.path))),
  )
  return (
    <>
      {listing?.stale && note(
        <span className="tree-updating" role="status" aria-label="Updating this folder">
          <LoaderCircle size={14} aria-hidden="true" />
          Updating
        </span>,
      )}
      {rows.length === 0 &&
        (top ? (
          q2 ? (
            <EmptyState title="No files match your search" body="Try a shorter or different name." />
          ) : (
            <EmptyState title="This folder is empty" body="Pick another place, or add files on the Files page." />
          )
        ) : (
          note(q2 ? 'Nothing here matches your search' : 'Nothing in this folder', ' faint')
        ))}
      {rows.map((e) => (
        <EntryRow key={e.name} e={e} level={level} />
      ))}
      {listing?.truncated && note(`Showing the first ${listing.entries.length} items`, ' faint')}
    </>
  )
}

/**
 * A place of the person's files (their home by default) as a tree: folders
 * open in place, read when first opened through the files API, with the
 * same rows, waiting and failure states and keyboard for every caller. The
 * caller gives each row its look (look) and says what activating one does
 * (a click on it, Space or Enter): choosing it in the file picker, ticking
 * it in the choice of what an agent can access.
 *
 * Folders on the way to openAt start open. expected is listed where it will
 * be when it is not there yet. Names that start with a dot are left out
 * unless showHidden, or keep says otherwise for one.
 */
export function HomeTree({ label, place = HOME_PLACE, query = '', showHidden, meta = false, keep, expected, openAt = [], reveal, look, onActivate }: {
  label: string
  place?: string
  query?: string
  showHidden: boolean
  /** Show each row's date and size. */
  meta?: boolean
  keep?: (path: string) => boolean
  expected?: { path: string; kind: 'dir' | 'file' }
  openAt?: string[]
  reveal?: string
  look: (e: TreeEntry) => RowLook
  /** What activating a row does; toggle opens or closes a folder. */
  onActivate: (e: TreeEntry, toggle: () => void) => void
}) {
  // Folders the person opened or closed; the rest are open when they lead to one of openAt, as it was at first.
  const [toggled, setToggled] = useState<Record<string, boolean>>({})
  const [leads] = useState(() => openAt)
  const [active, setActive] = useState<string | null>(null)
  const tree = useRef<HTMLDivElement>(null)
  const startsOpen = (path: string) => leads.some((p) => p.startsWith(`${path}/`))
  const isOpen = (path: string) => toggled[path] ?? startsOpen(path)
  // A new place starts closed again.
  useEffect(() => {
    setToggled({})
    setActive(null)
  }, [place])
  // The first row is the tab stop until another is focused.
  const [first, setFirst] = useState<string | null>(null)
  useEffect(() => {
    const el = tree.current?.querySelector<HTMLElement>('[role=treeitem]')
    const name = el?.getAttribute('aria-label') ?? null
    if (name !== first) setFirst(name)
  })
  const t: Tree = {
    place,
    query,
    showHidden,
    meta,
    keep,
    expected,
    look,
    onActivate,
    isOpen,
    toggle: (path) => setToggled((m) => ({ ...m, [path]: !(m[path] ?? startsOpen(path)) })),
    active,
    setActive,
    first,
    reveal,
  }

  // Arrow keys move between rows and open or close a folder; Space or Enter activates the row.
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const at = e.target as HTMLElement
    if (at.getAttribute('role') !== 'treeitem' || !tree.current) return
    const rows = [...tree.current.querySelectorAll<HTMLElement>('[role=treeitem]')]
    const i = rows.indexOf(at)
    const expanded = at.getAttribute('aria-expanded')
    const twist = () => at.querySelector<HTMLElement>(':scope > .tree-line > button.tree-twist')?.click()
    const level = Number(at.getAttribute('aria-level'))
    const act: Record<string, () => void> = {
      ArrowDown: () => rows[i + 1]?.focus(),
      ArrowUp: () => rows[i - 1]?.focus(),
      Home: () => rows[0]?.focus(),
      End: () => rows[rows.length - 1]?.focus(),
      ArrowRight: () => (expanded === 'false' ? twist() : expanded === 'true' && rows[i + 1]?.focus()),
      ArrowLeft: () =>
        expanded === 'true'
          ? twist()
          : rows
              .slice(0, i)
              .reverse()
              .find((r) => Number(r.getAttribute('aria-level')) < level)
              ?.focus(),
      ' ': () => at.click(),
      Enter: () => at.click(),
    }
    if (!act[e.key]) return
    e.preventDefault()
    act[e.key]()
  }

  return (
    <TreeContext.Provider value={t}>
      <div className="tree" role="tree" aria-label={label} ref={tree} onKeyDown={onKeyDown}>
        <FolderRows dir="" level={1} />
      </div>
    </TreeContext.Provider>
  )
}
