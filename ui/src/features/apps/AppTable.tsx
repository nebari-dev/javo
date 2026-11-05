import { Link } from 'react-router'
import type { SharedApp, Workspace } from '../../api'
import { useTemplateName } from '../../lib/queries'
import { appLink, heldStep, pillStatus, problemNote, stopNote } from './status'
import { RunActions } from './AppCard'
import { DeleteAppButton } from './DeleteApp'
import { listedKey, type ListedApp } from './listed'
import { OpenAppLink } from './OpenAppLink'
import { OthersAppActions } from './OthersAppActions'
import { sharedHow } from './SharedCard'
import { KindLogo } from '../../components/KindLogo'
import { Pill } from '../../components/Pill'
import { Uptime } from './Uptime'
import { PromptLine } from './AgentPrompt'

function AppRow({ ws }: { ws: Workspace }) {
  const template = useTemplateName(ws.template)
  const problem = problemNote(ws)
  const stopped = stopNote(ws) ?? heldStep(ws)?.title
  return (
    <tr>
      <th scope="row">
        <div className="atable-app">
          <KindLogo kind={ws.kind} />
          <div className="atable-name">
            <Link to={`/apps/${encodeURIComponent(ws.name)}`}>{ws.displayName}</Link>
            <PromptLine prompt={ws.agentPrompt} className="atable-why atable-prompt" />
            {problem && <span className={`atable-why ${problem.tone}`}>{problem.title}</span>}
            {stopped && <span className="atable-why warn">{stopped}</span>}
          </div>
        </div>
      </th>
      <td className="col-type">{template}</td>
      <td>
        <Pill status={pillStatus(ws.phase)} /> <Uptime ws={ws} />
      </td>
      <td className="col-file mono">{ws.file}</td>
      <td className="col-act">
        <div className="btn-row">
          <RunActions ws={ws} />
          <DeleteAppButton ws={ws} />
        </div>
      </td>
    </tr>
  )
}

/** An app another person shared: it opens the app itself, the only thing a viewer may do; a platform admin may also start, stop and delete it. */
function SharedRow({ app }: { app: SharedApp }) {
  const template = useTemplateName(app.template)
  const open = app.phase === 'Running' ? appLink(app.url) : undefined
  return (
    <tr>
      <th scope="row">
        <div className="atable-app">
          <KindLogo kind={app.kind} />
          <div className="atable-name">
            {open ? (
              <a href={open} target="_blank" rel="noopener">
                {app.displayName}
              </a>
            ) : (
              <span className="atable-title">{app.displayName}</span>
            )}
            <PromptLine prompt={app.agentPrompt} className="atable-why atable-prompt" />
            <span className="atable-why warn">
              from {app.owner.displayName} · <span>{sharedHow(app.via)}</span>
            </span>
          </div>
        </div>
      </th>
      <td className="col-type">{template}</td>
      <td>
        <Pill status={pillStatus(app.phase)} />
      </td>
      <td className="col-file mono" />
      <td className="col-act">
        <div className="btn-row">
          {open && (
            <OpenAppLink className="btn sm" href={open}>
              <span className="atable-act-label">Open</span>
            </OpenAppLink>
          )}
          <OthersAppActions app={app} />
        </div>
      </td>
    </tr>
  )
}

/** The user's apps and shared ones as one compact table; scrolls inside its own box on narrow screens. */
export function AppTable({ apps }: { apps: ListedApp[] }) {
  return (
    <div className="card atable-wrap">
      <table className="atable">
        <caption className="sr-only">Apps</caption>
        <thead>
          <tr>
            <th scope="col">App</th>
            <th scope="col" className="col-type">
              Type
            </th>
            <th scope="col">Status</th>
            <th scope="col" className="col-file">
              File
            </th>
            <th scope="col" className="col-act">
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {apps.map((a) => (a.mine ? <AppRow key={listedKey(a)} ws={a.mine} /> : <SharedRow key={listedKey(a)} app={a.shared} />))}
        </tbody>
      </table>
    </div>
  )
}
