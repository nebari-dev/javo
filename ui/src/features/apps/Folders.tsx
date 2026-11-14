import { Folder, Lock, Plus, Shield } from 'lucide-react'
import { useState } from 'react'
import type { FolderGrant, FoldersView, Workspace } from '../../api'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { Segmented } from '../../components/Segmented'
import { fieldErrors, problemText } from '../../lib/http'
import { useSetFolders } from '../../lib/queries'
import { HomePicker } from '../files/HomePicker'
import { agentOf } from './agents'

type Mode = FolderGrant['mode']

/** Why saving the list failed: the server's words about the folder, else about the request. */
const whyFailed = (err: unknown) => fieldErrors(err).fields.folders ?? problemText(err)

const MODE_LABEL: Record<Mode, string> = { read: 'Read only', write: 'Read and change' }

/** True when path is dir or inside it. */
const within = (dir: string, path: string) => path === dir || path.startsWith(`${dir}/`)

/**
 * Why the path of the home cannot be added to what the agent can open, or
 * undefined when it can: it is open already, or (never) is, is in or holds one
 * of the places the API says no agent is let into. The API decides again on save.
 */
function whyNot(view: FoldersView, path: string): { why: string; never?: boolean } | undefined {
  if (view.project.startsWith('~/') && within(view.project.slice(2), path)) return { why: 'already open' }
  if ((view.grants ?? []).some((g) => g.path === path)) return { why: 'already added' }
  for (const n of view.never ?? []) {
    if (within(n.path, path)) return { why: n.why, never: true }
    if (within(path, n.path)) return { why: `holds ~/${n.path}, which no agent can open`, never: true }
  }
  return undefined
}

function ModeTag({ mode }: { mode: Mode }) {
  return <span className={`chip ${mode === 'write' ? 'acc' : 'idle'}`}>{MODE_LABEL[mode]}</span>
}

/** The dialog that adds one folder or file of the home, to read unless the person picks more. */
function AddFolder({ agent, view, busy, error, onAdd, onClose }: {
  agent: string
  view: FoldersView
  busy: boolean
  error: unknown
  onAdd: (grant: FolderGrant) => void
  onClose: () => void
}) {
  const [mode, setMode] = useState<Mode>('read')
  return (
    <HomePicker
      folders
      filesToo
      extensions={[]}
      acceptReason=""
      title={`Let ${agent} open another folder`}
      subtitle="Pick a folder or file in your home."
      refused={(path) => whyNot(view, path)}
      chooseLabel={(path) => (path === null ? 'Pick a folder or file' : `Let ${agent} ${mode === 'read' ? 'read' : 'read and change'} ~/${path}`)}
      options={
        <>
          <div className="field fit tight">
            <span className="field-label">{agent} can</span>
            <Segmented
              label={`${agent} can`}
              value={mode}
              onChange={setMode}
              options={[
                { value: 'read', label: 'Only read it' },
                { value: 'write', label: 'Read and change it' },
              ]}
            />
          </div>
          <Banner tone="info" title={`${agent} restarts to pick this up`}>
            It takes a few seconds.
          </Banner>
        </>
      }
      busy={busy}
      failure={error ? whyFailed(error) : undefined}
      onChoose={(path) => onAdd({ path, mode })}
      onClose={onClose}
    />
  )
}

/**
 * What a coding agent that starts locked in can open: its project, which
 * stays, and the folders and files of the home its owner added, each to read
 * or to read and change. Nothing for an app that is not locked in (the API
 * sends no folders).
 */
export function FoldersCard({ ws }: { ws: Workspace }) {
  const set = useSetFolders(ws.name)
  const [adding, setAdding] = useState(false)
  const view = ws.folders
  if (!view) return null
  const agent = agentOf(ws)
  const grants = view.grants ?? []
  return (
    <Card className="pad open-card">
      <h3 className="label">
        <Shield size={15} aria-hidden="true" className="part-icon" />
        What {agent} can open
      </h3>
      <p className="tight muted small">
        {agent}, and every command it runs, can open this app's project and what you add here, besides its own settings and the system. Everything
        else in your home is hidden from it. Adding or removing something restarts {agent}, which takes a few seconds.
      </p>
      <ul className="open-list">
        <li className="open-row">
          <Folder size={18} aria-hidden="true" />
          <span className="open-what">
            <code>{view.project}</code>
            <span className="muted small">This app's project</span>
          </span>
          <ModeTag mode="write" />
          <span className="open-fixed" role="img" aria-label="Always open" title={`The project is always open to ${agent}`}>
            <Lock size={16} aria-hidden="true" />
          </span>
        </li>
        {grants.map((g) => (
          <li key={g.path} className="open-row">
            <Folder size={18} aria-hidden="true" />
            <span className="open-what">
              <code>~/{g.path}</code>
              <span className="muted small">Added by you</span>
            </span>
            <ModeTag mode={g.mode} />
            <Button
              size="sm"
              variant="ghost"
              aria-label={`Remove ~/${g.path}`}
              disabled={set.isPending}
              onClick={() => set.mutate(grants.filter((o) => o.path !== g.path))}
            >
              Remove
            </Button>
          </li>
        ))}
      </ul>
      <Button icon={Plus} onClick={() => setAdding(true)}>
        Add a folder or file
      </Button>
      {set.isError && !adding && <span className="errmsg">{whyFailed(set.error)}</span>}
      {adding && (
        <AddFolder
          agent={agent}
          view={view}
          busy={set.isPending}
          error={set.isError ? set.error : undefined}
          onAdd={(grant) => set.mutate([...grants, grant], { onSuccess: () => setAdding(false) })}
          onClose={() => {
            set.reset()
            setAdding(false)
          }}
        />
      )}
    </Card>
  )
}
