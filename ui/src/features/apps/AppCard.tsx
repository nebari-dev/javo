import { Play } from 'lucide-react'
import { OpenAppLink } from './OpenAppLink'
import { Link } from 'react-router'
import type { Workspace } from '../../api'
import { problemText } from '../../lib/http'
import { useSizes, useStartStop, useTemplateName } from '../../lib/queries'
import { runsAt } from '../../lib/sizes'
import { appLink, heldStep, pillStatus, problemNote, stopNote } from './status'
import { Button } from '../../components/Button'
import { CardMark } from './CardThumb'
import { EmptyState } from '../../components/EmptyState'
import { StatusIcon } from '../../components/Pill'
import { StopButton } from './StopButton'
import { Uptime } from './Uptime'

/** Open, Start or Stop for one app, shared by its card and its table row. */
export function RunActions({ ws }: { ws: Workspace }) {
  const act = useStartStop(ws.name)
  const open = ws.phase === 'Running' ? appLink(ws.url) : undefined
  // A restored app waits for an admin: it can neither start nor stop.
  if (heldStep(ws)) return null
  return (
    <>
      {open && (
        <OpenAppLink className="btn sm" href={open}>
          <span className="atable-act-label">Open</span>
        </OpenAppLink>
      )}
      {ws.phase === 'Stopped' && (
        <Button size="sm" icon={Play} disabled={act.isPending} onClick={() => act.mutate('start')}>
          <span className="atable-act-label">Start</span>
        </Button>
      )}
      {ws.phase !== 'Stopped' && ws.phase !== 'Stopping' && <StopButton ws={ws} act={act} size="sm" />}
      {act.isError && <span className="errmsg block">{problemText(act.error)}</span>}
    </>
  )
}

export function AppCard({ ws }: { ws: Workspace }) {
  const problem = problemNote(ws)
  const stopped = stopNote(ws) ?? heldStep(ws)?.title
  const template = useTemplateName(ws.template)
  const specs = runsAt(ws, useSizes().data ?? [])
  return (
    <div className="card acard">
      <div className="acard-head">
        <CardMark kind={ws.kind} thumbnail={ws.thumbnail} />
        <div className="grow">
          {/* The name's link covers the whole card; the buttons sit above it. */}
          <Link to={`/apps/${encodeURIComponent(ws.name)}`} className="acard-link">
            <h3>{ws.displayName}</h3>
          </Link>
          <div className="acard-sub">
            <span>{template}</span>
            {specs && <> · <span>{specs}</span></>} <Uptime ws={ws} />
          </div>
        </div>
        <StatusIcon status={pillStatus(ws.phase)} />
      </div>
      {problem ? (
        <div className={`why ${problem.tone}`}>
          <b>{problem.title}</b>
        </div>
      ) : (
        stopped && (
          <div className="meta" title={stopped}>
            {stopped}
          </div>
        )
      )}
      <div className="acard-foot">
        <div className="acard-actions">
          <RunActions ws={ws} />
        </div>
      </div>
    </div>
  )
}

// Shown on Home and Apps when the user has no apps.
export function NoApps() {
  return (
    <EmptyState
      title="No apps yet"
      body="Start JupyterLab, VS Code or a Streamlit app in a few clicks. Apps you create show up here."
      action={{ label: 'Create your first app', to: '/apps/new' }}
    />
  )
}
