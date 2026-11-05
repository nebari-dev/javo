import { CircleStop, Play, Trash, type LucideIcon } from 'lucide-react'
import { useRef, useState, type MouseEvent, type ReactNode, type RefObject } from 'react'
import type { SharedApp } from '../../api'
import { Button } from '../../components/Button'
import { Confirm } from '../../components/Confirm'
import { IconButton } from '../../components/IconButton'
import { useAdminDelete, useAdminStart, useAdminStop, useIsAdmin } from '../settings/admin/queries'

type Change = ReturnType<typeof useAdminStart> | ReturnType<typeof useAdminStop> | ReturnType<typeof useAdminDelete>

const STOPPABLE = new Set(['Running', 'Starting', 'Pending', 'Failed'])

/** The owner's name as the confirmations show it. */
const Owner = ({ app }: { app: SharedApp }) => <span className="confirm-owner">{app.owner.displayName}</span>

/** A control that asks first, then makes change on app for its owner. */
function Asked(props: {
  app: SharedApp
  change: Change
  title: ReactNode
  body: string
  confirm: string
  icon: LucideIcon
  danger?: boolean
  control: (ref: RefObject<HTMLButtonElement | null>, ask: () => void) => ReactNode
}) {
  const { app, change } = props
  const [asking, setAsking] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  return (
    <span className="stop-wrap">
      {props.control(btn, () => setAsking(true))}
      {asking && (
        <Confirm
          title={props.title}
          body={props.body}
          confirm={props.confirm}
          icon={props.icon}
          danger={props.danger}
          busy={change.isPending}
          error={change.isError ? change.error : undefined}
          onConfirm={() => change.mutate({ name: app.name, owner: app.owner.id }, { onSuccess: () => setAsking(false) })}
          onCancel={() => {
            setAsking(false)
            btn.current?.focus()
          }}
        />
      )}
    </span>
  )
}

/**
 * Start or Stop, and Delete, on an app another person shared, for a platform
 * admin; nothing for anyone else. Each asks first and names the owner.
 */
export function OthersAppActions({ app }: { app: SharedApp }) {
  const admin = useIsAdmin()
  const start = useAdminStart()
  const stop = useAdminStop()
  const del = useAdminDelete()
  if (!admin) return null
  const who = app.owner.displayName
  // The card's link covers the card; a click on a control must not open the app.
  const press = (ask: () => void) => (e: MouseEvent) => {
    e.stopPropagation()
    ask()
  }
  return (
    <>
      {app.phase === 'Stopped' && (
        <Asked
          app={app}
          change={start}
          title={<>Start <Owner app={app} />'s app {app.displayName}?</>}
          body={`It runs as ${who}, and its CPU and memory count toward ${who}'s limits.`}
          confirm="Start"
          icon={Play}
          control={(ref, ask) => (
            <Button ref={ref} size="sm" icon={Play} aria-label={`Start ${app.displayName}`} disabled={start.isPending} onClick={press(ask)}>
              <span className="atable-act-label">Start</span>
            </Button>
          )}
        />
      )}
      {STOPPABLE.has(app.phase) && (
        <Asked
          app={app}
          change={stop}
          title={<>Stop <Owner app={app} />'s app {app.displayName}?</>}
          body={`It shuts down for ${who} and everyone it is shared with. ${who} sees that an admin stopped it; their files and settings are kept.`}
          confirm="Stop"
          icon={CircleStop}
          danger
          control={(ref, ask) => (
            <Button ref={ref} size="sm" icon={CircleStop} aria-label={`Stop ${app.displayName}`} disabled={stop.isPending} onClick={press(ask)}>
              <span className="stop-label">Stop</span>
            </Button>
          )}
        />
      )}
      <Asked
        app={app}
        change={del}
        title={<>Delete {app.displayName} of <Owner app={app} />?</>}
        body={`This app belongs to ${who} (shared with you). Deleting it removes it for ${who} and everyone it is shared with.`}
        confirm="Delete"
        icon={Trash}
        danger
        control={(ref, ask) => <IconButton ref={ref} icon={Trash} label={`Delete ${app.displayName}`} className="danger" onClick={press(ask)} />}
      />
    </>
  )
}
