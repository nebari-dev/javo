import { Check, X } from 'lucide-react'
import { useState } from 'react'
import { useDecidePlan } from './ai'
import type { Change, Plan } from '../../api'
import { problemText } from '../../lib/http'
import { Button } from '../../components/Button'

/** What a change does, field by field: the old value struck out, the new one added. */
function Diff({ changes }: { changes: Change[] | null }) {
  return (
    <dl className="diff" aria-label="Changes">
      {(changes ?? []).map((c) => (
        <div className="diff-row" key={c.field}>
          <dt>{c.field}</dt>
          <dd>
            <del aria-label={`from ${c.from || 'nothing'}`}>{c.from || 'nothing'}</del>
            <ins aria-label={`to ${c.to}`}>{c.to}</ins>
          </dd>
        </div>
      ))}
    </dl>
  )
}

const DONE: Record<string, string> = {
  applied: 'Done.',
  rejected: 'Turned down. Nothing changed.',
}

/**
 * A plan to review: its summary, the diff and Apply or Cancel. Apply makes
 * the change; Cancel turns it down. The server says whether it applied.
 */
export function PlanReview({
  plan,
  applyLabel = 'Apply',
  cancelLabel = 'Cancel',
  onDone,
}: {
  plan: Plan
  applyLabel?: string
  cancelLabel?: string
  onDone?: (p: Plan) => void
}) {
  const decide = useDecidePlan()
  const [current, setCurrent] = useState(plan)
  const pending = current.state === 'pending'
  const run = (approve: boolean) =>
    decide.mutate(
      { id: current.id, approve },
      {
        onSuccess: (p) => {
          setCurrent(p)
          onDone?.(p)
        },
      },
    )
  return (
    <div className="plan" aria-label={current.planned.summary}>
      <div className="plan-summary">{current.planned.summary}</div>
      <Diff changes={current.planned.changes} />
      {decide.isError && (
        <p className="plan-error" role="alert">
          {problemText(decide.error)}
        </p>
      )}
      {current.state === 'failed' && current.error && !decide.isError && (
        <p className="plan-error" role="alert">
          {current.error}
        </p>
      )}
      {!pending && DONE[current.state] && <p className="plan-note">{DONE[current.state]}</p>}
      {current.message && <p className="plan-note">{current.message}</p>}
      {pending && (
        <div className="row plan-actions">
          <Button
            size="sm"
            variant={current.planned.destructive ? 'danger' : 'primary'}
            icon={Check}
            disabled={decide.isPending}
            onClick={() => run(true)}
          >
            {applyLabel}
          </Button>
          <Button size="sm" variant="ghost" icon={X} disabled={decide.isPending} onClick={() => run(false)}>
            {cancelLabel}
          </Button>
        </div>
      )}
    </div>
  )
}
