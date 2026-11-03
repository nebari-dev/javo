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
