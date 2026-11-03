import { Check } from 'lucide-react'
import type { ReactNode } from 'react'
import { More } from './More'

export type StepState = 'done' | 'now' | 'bad' | 'todo'

export interface TimelineStep {
  title: ReactNode
  detail?: ReactNode
  time?: string
  state: StepState
  /** What really happened in this step (events, log lines), behind a disclosure. */
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

export function Timeline({ steps, label }: { steps: TimelineStep[]; label?: string }) {
  return (
    <ol className="tl" aria-label={label}>
      {steps.map((s, i) => (
        <li key={i} data-state={s.state} className={s.state} aria-current={s.state === 'now' ? 'step' : undefined}>
          <div className="n" aria-hidden="true">
            {s.state === 'done' ? <Check size={14} /> : s.state === 'bad' ? '!' : i + 1}
          </div>
          <div>
            <div className="t">
              {s.title} <span className="sr-only">{STATE_LABELS[s.state]}</span>
            </div>
            {s.detail && <div className="d">{s.detail}</div>}
            {s.logs && (
              // Remounts when the step changes state, so a step that fails later opens too.
              <More key={s.state} closed="Show events and log lines" open="Hide events and log lines" defaultOpen={s.logsOpen}>
                {s.logs}
              </More>
            )}
          </div>
          {s.time && <div className="time">{s.time}</div>}
        </li>
      ))}
    </ol>
  )
}
