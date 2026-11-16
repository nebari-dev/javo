import { Check, ChevronRight, File, Folder, Lock, X } from 'lucide-react'
import { Fragment, useId, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import type { FolderGrant, FoldersView, Workspace } from '../../api'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { Segmented } from '../../components/Segmented'
import { Switch } from '../../components/Switch'
import type { HomeEntry } from '../../lib/files'
import { ApiError, fieldErrors, problemText } from '../../lib/http'
import { useHomeFolder, useSetFolders } from '../../lib/queries'
import { agentOf } from './agents'
import { MAX_GRANTS, openTo, placeOf, projectPath, rowOf, sameGrants, within, withGrant, type Mode, type Row } from './folderAccess'

/** Why saving the list failed: the server's words about the folder, else about the request. */
const whyFailed = (err: unknown) => fieldErrors(err).fields.folders ?? problemText(err)

const MODE_LABEL: Record<Mode, string> = { read: 'Read only', write: 'Read and change' }

/** What the rows of the tree share: whose choice it is, what is ticked, and what a click does. */
interface Browser {
  agent: string
  view: FoldersView
  grants: FolderGrant[]
  /** Whether the folder at path shows what is in it. */
  isOpen: (path: string) => boolean
  toggle: (path: string) => void
  tick: (path: string) => void
  setMode: (path: string, mode: Mode) => void
  /** Whether what is hidden at the top of the home is listed; what leads to the project or to something ticked always is. */
  shows: (path: string) => boolean
  /** The risky place whose tick waits for the warning under its row to be answered. */
  asking: string | null
  answer: (allow: boolean) => void
  /** What the last click on a row that cannot change was answered with, shown under that row. */
  said: { path: string; text: ReactNode } | null
  /** The row arrow keys start from: the one last focused, or the first. */
  active: string | null
  setActive: (path: string) => void
}

const join = (dir: string, name: string) => (dir ? `${dir}/${name}` : name)

/** The words beside a row's name: why it is open, or what it holds. */
function whyOf(r: Row, agent: string, made: boolean): string {
  switch (r.is) {
    case 'project':
      return `This app's project${made ? ', made when the app starts' : ''}. ${agent} can always read and change it`
    case 'fixed':
      return `${r.place.what}. ${agent} always has access`
    case 'shut':
      return r.place.rule === 'around' ? (r.place.means ?? '') : [`Holds ${r.place.what}.`, r.place.means].filter(Boolean).join(' ')
    case 'added':
      return r.place ? `Holds ${r.place.what}. You gave ${agent} access` : ''
    case 'included':
      return `Included with ~/${r.from}`
    case 'free':
      return r.place ? `Holds ${r.place.what}. Asks you first` : ''
  }
}

/** One folder or file of the home: its tick box, its name, why it is open or closed, and how open it is. */
function EntryRow({ b, dir, entry, level, first, made }: { b: Browser; dir: string; entry: HomeEntry; level: number; first: boolean; made: boolean }) {
  const askId = useId()
  const path = join(dir, entry.name)
  const r = rowOf(b.view, b.grants, path)
  const mode = openTo(r)
  const folder = entry.kind === 'folder'
  // Nothing in a place no agent opens can be ticked, so it does not open; a folder around one does.
  const opens = folder && !(r.is === 'shut' && r.place.rule === 'closed')
  const open = opens && b.isOpen(path)
  const why = whyOf(r, b.agent, made)
  const ask = b.asking === path && r.is === 'free' ? r.place : undefined
  const look = mode ? ` lit ${r.is !== 'project' && r.is !== 'fixed' && r.place ? 'risky' : mode}` : ''
  const Icon = folder ? Folder : File
  return (
    <>
      <div
        role="treeitem"
        aria-label={entry.name}
        aria-level={level}
        aria-expanded={opens ? open : undefined}
        tabIndex={b.active === path || (b.active === null && first) ? 0 : -1}
        className={`hb-row${look}`}
        style={{ '--hb-level': level - 1 } as React.CSSProperties}
        onFocus={() => b.setActive(path)}
      >
        <div className="hb-line">
          {opens ? (
            <button type="button" className="hb-twist" tabIndex={-1} aria-label={`${open ? 'Collapse' : 'Expand'} ${entry.name}`} onClick={() => b.toggle(path)}>
              <ChevronRight size={16} aria-hidden="true" />
            </button>
          ) : (
            <span className="hb-twist" />
          )}
          {r.is === 'shut' ? (
            <Lock size={16} aria-hidden="true" className="hb-shut" />
          ) : (
            <input
              type="checkbox"
              className="hb-tick"
              tabIndex={-1}
              checked={!!mode}
              aria-disabled={r.is === 'added' || r.is === 'free' ? undefined : true}
              aria-label={`Let ${b.agent} access ~/${path}`}
              onChange={() => b.tick(path)}
            />
          )}
          <Icon size={18} aria-hidden="true" className="hb-icon" />
          <span className="hb-name">
            <span className="mono">{entry.name}</span>
            {why && <span className="hb-why">{why}</span>}
          </span>
          {r.is === 'added' ? (
            <Segmented
              label={`What ${b.agent} can do in ~/${path}`}
              value={r.mode}
              onChange={(m) => b.setMode(path, m)}
              options={[
                { value: 'read', label: MODE_LABEL.read },
                { value: 'write', label: MODE_LABEL.write },
              ]}
            />
          ) : mode ? (
            <span className="hb-mode">{MODE_LABEL[mode]}</span>
          ) : (
            r.is === 'free' && r.place && <span className="chip warn">Risky</span>
          )}
        </div>
        {ask && (
          <div className="hb-ask" role="group" aria-labelledby={askId}>
            <strong id={askId}>
              Let {b.agent} read {ask.what} in <code>~/{path}</code>?
            </strong>
            <ul>
              <li>{b.agent}, and every command it runs, will be able to read them.</li>
              {ask.means && <li>{ask.means}</li>}
              <li>{b.agent} can use the internet. If a web page, file or package tricks it, these files can be sent anywhere.</li>
            </ul>
            <div className="hb-ask-actions">
              <Button size="sm" onClick={() => b.answer(false)}>
                Keep no access
              </Button>
              <Button size="sm" variant="danger" onClick={() => b.answer(true)}>
                Let {b.agent} read {ask.what}
              </Button>
            </div>
          </div>
        )}
        {b.said?.path === path && (
          <p className="hb-said tight small" role="status">
            {b.said.text}
          </p>
        )}
      </div>
      {open && <FolderRows b={b} dir={path} level={level + 1} absent={made} />}
    </>
  )
}

/**
 * What is in one folder of the home, read when the folder is first shown.
 * The project is listed where it will be even before it is made (absent: the
 * folder itself is not there yet, so it is not read).
 */
function FolderRows({ b, dir, level, absent = false }: { b: Browser; dir: string; level: number; absent?: boolean }) {
  const q = useHomeFolder(dir, !absent)
  const project = projectPath(b.view)
  // The next name on the way from here to the project.
  const step = project && (dir === '' || project.startsWith(`${dir}/`)) ? project.slice(dir ? dir.length + 1 : 0).split('/')[0] : undefined
  const note = (text: ReactNode, err = false) => (
    <div className={`hb-note${err ? ' errmsg' : ''}`} role={err ? 'alert' : undefined} style={{ '--hb-level': level - 1 } as React.CSSProperties}>
      {text}
    </div>
  )
  let entries: HomeEntry[] = []
  if (!absent) {
    const gone = q.error instanceof ApiError && q.error.status === 404 && step !== undefined
    if (q.isError && !gone) return note(problemText(q.error), true)
    if (q.isPending) return note('Loading')
    if (q.data?.kind === 'opening') return note(q.data.reason.title ?? 'Your home folder is getting ready')
    if (q.data?.kind === 'listed') entries = q.data.folder.entries
  }
  const missing = step !== undefined && !entries.some((e) => e.name === step)
  const rows = (missing ? [{ name: step, kind: 'folder' as const, size: 0, modified: '' }, ...entries] : entries).filter((e) => dir !== '' || b.shows(e.name))
  if (rows.length === 0) return note('Nothing in this folder')
  return (
    <>
      {rows.map((e, i) => (
        <EntryRow key={e.name} b={b} dir={dir} entry={e} level={level} first={dir === '' && i === 0} made={absent || (missing && i === 0)} />
      ))}
    </>
  )
}

const home = (path: string) => <code>~/{path}</code>

/** items with a comma, or the word, between them. */
const listed = (items: ReactNode[], word = ', ') => items.flatMap((x, i) => (i ? [word, x] : [x]))

/**
 * What the choice comes to, in whole sentences with the real paths: what the
 * agent can do, and what it cannot, down to the folders at the top of the
 * home it sees nothing of and the risky places not allowed.
 */
function CanCannot({ agent, view, grants }: { agent: string; view: FoldersView; grants: FolderGrant[] }) {
  const top = useHomeFolder('', true).data
  const names = top?.kind === 'listed' ? top.folder.entries : []
  const project = projectPath(view)
  const edit = (where: ReactNode) => <>Read, edit, add and delete files in {where}</>
  const can = [
    <Fragment key="project">{edit(<code>{view.project}</code>)}</Fragment>,
    ...grants.map((g) => {
      const place = placeOf(view, g.path)
      return (
        <Fragment key={g.path}>
          {place ? (
            <>
              <b>Risky:</b> {g.mode === 'write' ? 'read and change' : 'read'} {place.what} in {home(g.path)}. {place.means}
            </>
          ) : g.mode === 'write' ? (
            edit(home(g.path))
          ) : (
            <>Read files in {home(g.path)}. It cannot change or delete them</>
          )}
        </Fragment>
      )
    }),
    <Fragment key="net">Use the internet. This choice covers files only.</Fragment>,
  ]
  // Folders at the top of the home that are not open as a whole: shut entirely, or but for what is ticked in them.
  const open = [...(project ? [project] : []), ...grants.map((g) => g.path)]
  const shut = names.filter((e) => e.kind === 'folder' && !placeOf(view, e.name) && !open.includes(e.name)).map((e) => e.name)
  const partly = shut.filter((n) => open.some((p) => p.startsWith(`${n}/`)))
  const wholly = shut.filter((n) => !partly.includes(n))
  // The risky places of this home that were not allowed: those at its top, and those the API names inside a folder there.
  const here = names.map((e) => e.name)
  const risky = [
    ...here.filter((n) => placeOf(view, n)?.rule === 'risky'),
    ...(view.places ?? []).filter((p) => p.rule === 'risky' && p.path.includes('/') && here.includes(p.path.split('/')[0])).map((p) => p.path),
  ].filter((p) => openTo(rowOf(view, grants, p)) === undefined)
  const cannot = [
    wholly.length > 0 && <Fragment key="wholly">Read or change anything in {listed(wholly.map(home))}</Fragment>,
    partly.length > 0 && <Fragment key="partly">Read or change anything in {listed(partly.map(home), ' or ')} except what is ticked there</Fragment>,
    <Fragment key="change">Change or delete any file outside the folders it may change</Fragment>,
    risky.length > 0 && (
      <Fragment key="risky">
        Read {listed(risky.map((p) => [home(p), ` (${placeOf(view, p)?.what})`]))}. You have not given it access
      </Fragment>
    ),
  ].filter((line) => !!line)
  return (
    <div className="hb-sum">
      <div className="cc">
        <div className="cc-can">
          <h4>
            {agent} can
          </h4>
          <ul>
            {can.map((line) => (
              <li key={line.key}>
                <Check size={16} aria-hidden="true" />
                <span>{line}</span>
              </li>
            ))}
          </ul>
        </div>
        <div className="cc-cannot">
          <h4>
            {agent} cannot
          </h4>
          <ul>
            {cannot.map((line) => (
              <li key={line.key}>
                <X size={16} aria-hidden="true" />
                <span>{line}</span>
              </li>
            ))}
          </ul>
        </div>
      </div>
      <p className="tight muted small">Read means look at what is in a file. Change means edit it, add new files, or delete it.</p>
    </div>
  )
}

/**
 * The person's home as a tree where a tick opens a folder or file to their
 * coding agent: the choice of what a locked agent can open, on the form that
 * makes the app and on the app's page. grants is the choice so far; every
 * click hands back the whole new list.
 */
export function FolderBrowser({ agent, view, grants, onChange }: {
  agent: string
  view: FoldersView
  grants: FolderGrant[]
  onChange: (grants: FolderGrant[]) => void
}) {
  // Folders the person opened or closed; the rest are open when they lead to the project or to what was ticked at first.
  const [toggled, setToggled] = useState<Record<string, boolean>>({})
  const [ticked] = useState(() => grants.map((g) => g.path))
  const [active, setActive] = useState<string | null>(null)
  const [said, setSaid] = useState<Browser['said']>(null)
  const [asking, setAsking] = useState<string | null>(null)
  const [showHidden, setShowHidden] = useState(false)
  const tree = useRef<HTMLDivElement>(null)
  const legendId = useId()
  const project = projectPath(view)
  const leads = (path: string) => [...(project ? [project] : []), ...ticked].some((p) => p.startsWith(`${path}/`))
  // A click anywhere in the tree closes the warning or explanation of the one before.
  const quiet = () => {
    setSaid(null)
    setAsking(null)
  }
  const b: Browser = {
    agent,
    view,
    grants,
    isOpen: (path) => toggled[path] ?? leads(path),
    toggle: (path) => {
      quiet()
      setToggled((t) => ({ ...t, [path]: !(t[path] ?? leads(path)) }))
    },
    shows: (path) => showHidden || !path.startsWith(view.hidden.path) || [...(project ? [project] : []), ...grants.map((g) => g.path)].some((p) => within(path, p)),
    tick: (path) => {
      const r = rowOf(view, grants, path)
      const say = (text: ReactNode) => setSaid({ path, text })
      quiet()
      if (r.is === 'added') onChange(grants.filter((g) => g.path !== path))
      else if (r.is === 'free' && withGrant(grants, path, false).length > MAX_GRANTS) {
        say(`You have ticked ${MAX_GRANTS} folders and files, the most one agent can have. Untick one to tick another.`)
      } else if (r.is === 'free' && r.place) setAsking(path)
      else if (r.is === 'free') onChange(withGrant(grants, path, false))
      else if (r.is === 'project') say(`This is the app's project. ${agent} always reads and changes it.`)
      else if (r.is === 'fixed') say(`${agent} always has access to this. It cannot be taken away here.`)
      else if (r.is === 'included' && r.from === project) say(`This is inside the app's project, which ${agent} always reads and changes.`)
      else if (r.is === 'included') {
        const from = <code>~/{r.from}</code>
        say(
          <>
            This is inside {from}, which is ticked, so {agent} has access to it too. To leave it out, untick {from} and tick only the folders you want.
          </>,
        )
      }
    },
    asking,
    answer: (allow) => {
      if (allow && asking !== null) onChange(withGrant(grants, asking, true))
      quiet()
    },
    setMode: (path, mode) => {
      quiet()
      onChange(grants.map((g) => (g.path === path ? { ...g, mode } : g)))
    },
    said,
    active,
    setActive,
  }

  // Arrow keys move between rows and open or close a folder; Space or Enter ticks the row.
  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const at = e.target as HTMLElement
    if (at.getAttribute('role') !== 'treeitem' || !tree.current) return
    const rows = [...tree.current.querySelectorAll<HTMLElement>('[role=treeitem]')]
    const i = rows.indexOf(at)
    const expanded = at.getAttribute('aria-expanded')
    const twist = () => at.querySelector<HTMLElement>('button.hb-twist')?.click()
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
      ' ': () => at.querySelector<HTMLElement>('input.hb-tick')?.click(),
      Enter: () => at.querySelector<HTMLElement>('input.hb-tick')?.click(),
    }
    if (!act[e.key]) return
    e.preventDefault()
    act[e.key]()
  }

  return (
    <div className="hb">
      <h3 className="hb-q">
        Which files and folders can {agent} access?
      </h3>
      <p className="tight muted small">Tick what {agent} may access. Highlighted rows: it has access. Dimmed rows: it has no access.</p>
      <div className="hb-box">
        <div className="hb-head">
          {/* A key, not controls: each entry is a sample of how a row of that kind looks. */}
          <div className="hb-legend">
            <span id={legendId}>Row colours:</span>
            <ul aria-labelledby={legendId}>
              <li className="write">{MODE_LABEL.write}</li>
              <li className="read">{MODE_LABEL.read}</li>
              <li>No access</li>
            </ul>
          </div>
          <Switch
            label="Show hidden files and folders"
            checked={showHidden}
            onChange={(on) => {
              quiet()
              setActive(null)
              setShowHidden(on)
            }}
          />
        </div>
        {project === undefined && (
          <div className="hb-row lit write hb-apart">
            <div className="hb-line">
              <Folder size={18} aria-hidden="true" className="hb-icon" />
              <span className="hb-name">
                <span className="mono">{view.project}</span>
                <span className="hb-why">{whyOf({ is: 'project', mode: 'write' }, agent, false)}</span>
              </span>
              <span className="hb-mode">{MODE_LABEL.write}</span>
            </div>
          </div>
        )}
        <div className="hb-tree" role="tree" aria-label="Your home" ref={tree} onKeyDown={onKeyDown}>
          <FolderRows b={b} dir="" level={1} />
        </div>
      </div>
      <CanCannot agent={agent} view={view} grants={grants} />
    </div>
  )
}

/**
 * What a coding agent that starts locked in can open, on its page: the
 * browser, with the changes kept here until its owner saves them, as saving
 * restarts the agent. Nothing for an app that is not locked in (the API sends
 * no folders).
 */
export function FoldersCard({ ws }: { ws: Workspace }) {
  const set = useSetFolders(ws.name)
  const view = ws.folders
  // null until something is ticked or unticked; then the list to save.
  const [draft, setDraft] = useState<FolderGrant[] | null>(null)
  if (!view) return null
  const agent = agentOf(ws)
  const saved = view.grants ?? []
  const changed = draft !== null && !sameGrants(draft, saved)
  const discard = () => {
    set.reset()
    setDraft(null)
  }
  return (
    <Card className="pad open-card">
      <FolderBrowser agent={agent} view={view} grants={draft ?? saved} onChange={setDraft} />
      {changed && (
        <div className="hb-save">
          <p className="tight small" role="status">
            {agent} restarts to pick this up. It takes a few seconds.
          </p>
          {set.isError && (
            <p className="tight errmsg" role="alert">
              {whyFailed(set.error)}
            </p>
          )}
          <div className="hb-save-actions">
            <Button size="sm" disabled={set.isPending} onClick={discard}>
              Discard changes
            </Button>
            <Button size="sm" variant="primary" disabled={set.isPending} onClick={() => set.mutate(draft, { onSuccess: discard })}>
              Save
            </Button>
          </div>
        </div>
      )}
    </Card>
  )
}
