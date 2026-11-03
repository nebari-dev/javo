import { Link } from 'react-router'
import type { Workspace } from '../../api'
import { useTemplateName } from '../../lib/queries'
import { heldStep, pillStatus, problemNote, stopNote } from './status'
import { RunActions } from './AppCard'
import { KindLogo } from '../../components/KindLogo'
import { Pill } from '../../components/Pill'
import { Uptime } from './Uptime'

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
        </div>
      </td>
    </tr>
  )
}

/** Apps as a compact table; scrolls inside its own box on narrow screens. */
export function AppTable({ apps }: { apps: Workspace[] }) {
  return (
    <div className="card atable-wrap">
      <table className="atable">
        <caption className="sr-only">Your apps</caption>
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
          {apps.map((w) => (
            <AppRow key={w.name} ws={w} />
          ))}
        </tbody>
      </table>
    </div>
  )
}
