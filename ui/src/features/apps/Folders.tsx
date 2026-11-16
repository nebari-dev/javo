import { Check, Folder, Lock, X } from 'lucide-react'
import { Fragment, useId, useState, type ReactNode } from 'react'
import type { FolderGrant, FoldersView, Workspace } from '../../api'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { Segmented } from '../../components/Segmented'
import { fieldErrors, problemText } from '../../lib/http'
import { useHomeFolder, useSetFolders } from '../../lib/queries'
import { HomeTree, TreeTools, useShowHidden, type RowLook, type TreeEntry } from '../files/HomeTree'
import { agentOf } from './agents'
import { MAX_GRANTS, openTo, placeOf, projectPath, rowOf, sameGrants, within, withGrant, type Mode, type Row } from './folderAccess'

/** Why saving the list failed: the server's words about the folder, else about the request. */
const whyFailed = (err: unknown) => fieldErrors(err).fields.folders ?? problemText(err)

const MODE_LABEL: Record<Mode, string> = { read: 'Read only', write: 'Read and change' }

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

/** The question under a risky row whose tick waits for an answer. */
function Ask({ agent, path, what, means, onAnswer }: { agent: string; path: string; what: string; means?: string; onAnswer: (allow: boolean) => void }) {
  const id = useId()
  return (
    <div className="hb-ask" role="group" aria-labelledby={id}>
      <strong id={id}>
        Let {agent} read {what} in <code>~/{path}</code>?
      </strong>
      <ul>
        <li>{agent}, and every command it runs, will be able to read them.</li>
        {means && <li>{means}</li>}
        <li>{agent} can use the internet. If a web page, file or package tricks it, these files can be sent anywhere.</li>
      </ul>
      <div className="hb-ask-actions">
        <Button size="sm" onClick={() => onAnswer(false)}>
          Keep no access
        </Button>
        <Button size="sm" variant="danger" onClick={() => onAnswer(true)}>
          Let {agent} read {what}
        </Button>
      </div>
    </div>
  )
}

/**
 * The person's home as a tree (HomeTree) where a tick gives their coding
 * agent access to a folder or file: the choice of what a locked agent can
 * access, on the form that makes the app and on the app's page. grants is
 * the choice so far; every click hands back the whole new list.
 */
export function FolderBrowser({ agent, view, grants, onChange }: {
  agent: string
  view: FoldersView
  grants: FolderGrant[]
  onChange: (grants: FolderGrant[]) => void
}) {
  // What the last click on a row that cannot change was answered with, shown under that row.
  const [said, setSaid] = useState<{ path: string; text: ReactNode } | null>(null)
  // The risky place whose tick waits for the warning under its row to be answered.
  const [asking, setAsking] = useState<string | null>(null)
  const [query, setQuery] = useState('')
  const [showHidden, setShowHidden] = useShowHidden()
  const [openAt] = useState(() => [...(projectPath(view) ? [projectPath(view)!] : []), ...grants.map((g) => g.path)])
  const legendId = useId()
  const project = projectPath(view)
  // A click anywhere in the tree closes the warning or explanation of the one before.
  const quiet = () => {
    setSaid(null)
    setAsking(null)
  }
  const tick = (path: string) => {
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
  }

  const look = (e: TreeEntry): RowLook => {
    const r = rowOf(view, grants, e.path)
    const mode = openTo(r)
    const why = whyOf(r, agent, !!e.made)
    const ask = asking === e.path && r.is === 'free' ? r.place : undefined
    return {
      className: mode ? `lit ${r.is !== 'project' && r.is !== 'fixed' && r.place ? 'risky' : mode}` : undefined,
      // Nothing in a place no agent opens can be ticked, so it does not open; a folder around one does.
      expands: !(r.is === 'shut' && r.place.rule === 'closed'),
      lead:
        r.is === 'shut' ? (
          <Lock size={16} aria-hidden="true" className="hb-shut" />
        ) : (
          <input
            type="checkbox"
            className="hb-tick"
            tabIndex={-1}
            checked={!!mode}
            aria-disabled={r.is === 'added' || r.is === 'free' ? undefined : true}
            aria-label={`Let ${agent} access ~/${e.path}`}
            onChange={() => tick(e.path)}
          />
        ),
      note: why || undefined,
      trail:
        r.is === 'added' ? (
          <Segmented
            label={`What ${agent} can do in ~/${e.path}`}
            value={r.mode}
            onChange={(m) => {
              quiet()
              onChange(grants.map((g) => (g.path === e.path ? { ...g, mode: m } : g)))
            }}
            options={[
              { value: 'read', label: MODE_LABEL.read },
              { value: 'write', label: MODE_LABEL.write },
            ]}
          />
        ) : mode ? (
          <span className="hb-mode">{MODE_LABEL[mode]}</span>
        ) : (
          r.is === 'free' && r.place && <span className="chip warn">Risky</span>
        ),
      below: (
        <>
          {ask && (
            <Ask
              agent={agent}
              path={e.path}
              what={ask.what}
              means={ask.means}
              onAnswer={(allow) => {
                if (allow) onChange(withGrant(grants, e.path, true))
                quiet()
              }}
            />
          )}
          {said?.path === e.path && (
            <p className="hb-said tight small" role="status">
              {said.text}
            </p>
          )}
        </>
      ),
    }
  }

  return (
    <div className="hb">
      <h3 className="hb-q">Which files and folders can {agent} access?</h3>
      <p className="tight muted small">Tick what {agent} may access. Highlighted rows: it has access. Dimmed rows: it has no access.</p>
      <div className="hb-box">
        <div className="hb-head">
          <TreeTools
            query={query}
            onQuery={setQuery}
            showHidden={showHidden}
            onShowHidden={(on) => {
              quiet()
              setShowHidden(on)
            }}
          />
          {/* A key, not controls: each entry is a sample of how a row of that kind looks. */}
          <div className="hb-legend">
            <span id={legendId}>Row colours:</span>
            <ul aria-labelledby={legendId}>
              <li className="write">{MODE_LABEL.write}</li>
              <li className="read">{MODE_LABEL.read}</li>
              <li>No access</li>
            </ul>
          </div>
        </div>
        <div className="hb-list">
          {project === undefined && (
            <div className="tree-row lit write">
              <div className="tree-line">
                <span className="tree-twist" />
                <Folder size={18} aria-hidden="true" className="tree-icon" />
                <span className="tree-name">
                  <span className="mono">{view.project}</span>
                  <span className="tree-note">{whyOf({ is: 'project', mode: 'write' }, agent, false)}</span>
                </span>
                <span className="hb-mode">{MODE_LABEL.write}</span>
              </div>
            </div>
          )}
          <HomeTree
            label="Your home"
            query={query}
            showHidden={showHidden}
            // What leads to the project or to something ticked stays listed with hidden names left out.
            keep={(path) => [...(project ? [project] : []), ...grants.map((g) => g.path)].some((p) => within(path, p))}
            expected={project ? { path: project, kind: 'dir' } : undefined}
            openAt={openAt}
            look={look}
            onActivate={(e) => tick(e.path)}
          />
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
    <Card className="pad">
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
