import { Square } from 'lucide-react'
import { useRef, useState } from 'react'
import type { AdminApp } from '../../../api'
import { Banner } from '../../../components/Banner'
import { Button } from '../../../components/Button'
import { Confirm } from '../../../components/Confirm'
import { EmptyState } from '../../../components/EmptyState'
import { KindLogo } from '../../../components/KindLogo'
import { Pill } from '../../../components/Pill'
import { problemText } from '../../../lib/http'
import { OpenAppLink } from '../../apps/OpenAppLink'
import { readySince } from '../../apps/startup'
import { appLink, pillStatus, stopNote } from '../../apps/status'
import { useAdminApps, useAdminStop } from './queries'
import { duration, when } from './format'

const STOPPABLE = new Set(['Running', 'Starting', 'Pending', 'Failed'])

/** "Running for 2 h 14 min" for a running app whose ready time is known; undefined otherwise. */
function runningFor(item: AdminApp): string | undefined {
  const since = item.app.phase === 'Running' ? readySince(item.app.steps ?? []) : undefined
  return since === undefined ? undefined : `Running for ${duration(since)}`
}

/** Display names that more than one person has: those owners get their username as a tooltip. */
function sharedNames(items: AdminApp[]): Set<string> {
  const people = new Map<string, Set<string>>()
  for (const { owner } of items) people.set(owner.displayName, (people.get(owner.displayName) ?? new Set()).add(owner.name))
  return new Set([...people].filter(([, names]) => names.size > 1).map(([display]) => display))
}

function StopFor({ item }: { item: AdminApp }) {
  const stop = useAdminStop()
  const [asking, setAsking] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const { app, owner } = item
  return (
    <span className="stop-wrap">
      <Button ref={btn} size="sm" icon={Square} aria-label={`Stop ${app.displayName}`} disabled={stop.isPending} onClick={() => setAsking(true)}>
        <span className="atable-act-label">Stop</span>
      </Button>
      {asking && (
        <Confirm
          title={`Stop ${app.displayName} of ${owner.displayName}?`}
          body={`${owner.displayName} sees that an admin stopped it. Their files and settings are kept, and they can start it again.`}
          confirm="Stop"
          icon={Square}
          danger
          busy={stop.isPending}
          error={stop.isError ? stop.error : undefined}
          onConfirm={() => stop.mutate({ name: app.name, owner: owner.name }, { onSuccess: () => setAsking(false) })}
          onCancel={() => {
            setAsking(false)
            btn.current?.focus()
          }}
        />
      )}
    </span>
  )
}

/** Settings, All apps: every person's apps, with Open and Stop for any of them. */
export function AdminApps() {
  const apps = useAdminApps()
  if (apps.isError) return <Banner tone="err" title={problemText(apps.error)} />
  if (apps.isPending) return <div className="muted">Loading</div>
  if (apps.data.length === 0) return <EmptyState title="No apps yet" body="Apps people create show here." />
  const shared = sharedNames(apps.data)
  return (
    <>
      <p className="sub">Every app on this install. Opening one is recorded in the audit log. Stopping one tells its owner an admin did it; their files and settings are kept.</p>
      <div className="section-sm card atable-wrap">
        <table className="atable">
          <caption className="sr-only">All apps</caption>
          <thead>
            <tr>
              <th scope="col">App</th>
              <th scope="col">Owner</th>
              <th scope="col">Status</th>
              <th scope="col" className="col-type">
                Last used
              </th>
              <th scope="col" className="col-act">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {apps.data.map((item) => {
              const note = stopNote(item.app)
              const running = runningFor(item)
              const open = item.app.phase === 'Running' ? appLink(item.app.url) : undefined
              return (
                <tr key={item.id}>
                  <th scope="row">
                    <div className="atable-app">
                      <KindLogo kind={item.app.kind} />
                      <div className="atable-name">
                        <span className="atable-title">{item.app.displayName}</span>
                        {note && <span className="atable-why warn">{note}</span>}
                      </div>
                    </div>
                  </th>
                  <td>
                    <span title={shared.has(item.owner.displayName) ? item.owner.username : undefined}>{item.owner.displayName}</span>
                  </td>
                  <td>
                    <div className="atable-status">
                      <Pill status={pillStatus(item.app.phase)} />
                      {running && <span className="atable-why warn">{running}</span>}
                    </div>
                  </td>
                  <td className="col-type">{when(item.lastActiveAt)}</td>
                  <td className="col-act">
                    <span className="atable-acts">
                      {open && (
                        <OpenAppLink className="btn sm" href={open} label={`Open ${item.app.displayName}`}>
                          <span className="atable-act-label">Open</span>
                        </OpenAppLink>
                      )}
                      {STOPPABLE.has(item.app.phase) && <StopFor item={item} />}
                    </span>
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </>
  )
}
