import { Check } from 'lucide-react'
import { useId, useState, type ReactNode } from 'react'
import { MoreToggle } from './More'

export type StepState = 'done' | 'now' | 'bad' | 'todo'

export interface TimelineStep {
  title: ReactNode
  detail?: ReactNode
  time?: string
  state: StepState
  /** What really happened in this step (events, log lines), behind the step's row. */
  logs?: ReactNode
  /** Open the logs from the start, e.g. for a failed step. */
  logsOpen?: boolean
}

const STATE_LABELS: Record<StepState, string> = {
  done: 'Done',
  now: 'In progress',
  bad: 'Failed',
  todo: 'Not started',
}

/** One step: its row (chevron, status, title, time) opens what happened in it. */
function Step({ step: s, n }: { step: TimelineStep; n: number }) {
  const [open, setOpen] = useState(!!s.logsOpen)
  // A step that changes state takes its new default, so a step that fails later opens too.
  const [seen, setSeen] = useState(s.state)
  if (seen !== s.state) {
    setSeen(s.state)
    setOpen(!!s.logsOpen)
  }
  const bodyId = useId()
  const row = (
    <>
      <span className="n" aria-hidden="true">
        {s.state === 'done' ? <Check size={14} /> : s.state === 'bad' ? '!' : n}
      </span>
      <span className="tl-text">
        <span className="t">
          {s.title} <span className="sr-only">{STATE_LABELS[s.state]}</span>
        </span>
        {s.detail && <span className="d">{s.detail}</span>}
      </span>
      {s.time && <span className="time">{s.time}</span>}
    </>
  )
  return (
    <li data-state={s.state} className={s.state} aria-current={s.state === 'now' ? 'step' : undefined}>
      {s.logs ? (
        <MoreToggle className="tl-row" open={open} onToggle={() => setOpen(!open)} controls={bodyId}>
          {row}
        </MoreToggle>
      ) : (
        <div className="tl-row">
          <span className="tl-nochev" aria-hidden="true" />
          {row}
        </div>
      )}
      {s.logs && (
        // Remounts when the step changes state, so its streams start over.
        <div key={s.state} id={bodyId} className="tl-body">
          {open && s.logs}
        </div>
      )}
    </li>
  )
}

export function Timeline({ steps, label }: { steps: TimelineStep[]; label?: string }) {
  return (
    <ol className="tl" aria-label={label}>
      {steps.map((s, i) => (
        <Step key={i} step={s} n={i + 1} />
      ))}
    </ol>
  )
}
