import { CircleAlert, CircleCheck, CircleDashed, CircleStop, type LucideIcon } from 'lucide-react'

export type Status = 'running' | 'starting' | 'stopping' | 'pending' | 'failed' | 'stopped'

const LABELS: Record<Status, string> = {
  running: 'Running',
  starting: 'Starting',
  stopping: 'Stopping',
  pending: 'Pending',
  failed: 'Failed',
  stopped: 'Stopped',
}

export function Pill({ status, label }: { status: Status; label?: string }) {
  return (
    <span className={`pill s-${status}`}>
      <span className="dot" aria-hidden="true" />
      {label ?? LABELS[status]}
    </span>
  )
}

const ICONS: Record<Status, LucideIcon> = {
  running: CircleCheck,
  starting: CircleDashed,
  stopping: CircleDashed,
  pending: CircleDashed,
  failed: CircleAlert,
  stopped: CircleStop,
}

/** A status as a small coloured icon, named and titled with its word. */
export function StatusIcon({ status }: { status: Status }) {
  const Icon = ICONS[status]
  return (
    <span className={`status-icon s-${status}`} role="img" aria-label={LABELS[status]} title={LABELS[status]}>
      <Icon size={18} aria-hidden="true" />
    </span>
  )
}
