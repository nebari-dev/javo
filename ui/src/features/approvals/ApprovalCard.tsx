import { ClipboardCheck, Clock3 } from 'lucide-react'
import type { Plan } from '../../api'
import { Card } from '../../components/Card'
import { PlanReview } from '../ai/Plan'
import { requestedBy } from '../ai/PlanPage'

/** When a pending plan stops being decidable, in words. */
export function expiresIn(expiresAt: string, now = Date.now()): string {
  const ms = new Date(expiresAt).getTime() - now
  if (ms <= 0) return 'Expired'
  return `Expires in ${Math.ceil(ms / 60_000)} min`
}

/** One plan waiting for the person: who asked, what changes, Approve or Deny. */
export function ApprovalCard({ plan, onFailed }: { plan: Plan; onFailed?: (p: Plan) => void }) {
  return (
    <Card className="pad approval" role="article" aria-label={plan.planned.summary}>
      <div className="approval-head">
        <div className="label">
          <ClipboardCheck size={15} aria-hidden="true" />
          {requestedBy(plan.source, plan.requestedBy)}
        </div>
        <span className="approval-expiry muted small">
          <Clock3 size={13} aria-hidden="true" />
          {expiresIn(plan.expiresAt)}
        </span>
      </div>
      <PlanReview plan={plan} applyLabel="Approve" cancelLabel="Deny" confirmDestructive onFailed={onFailed} />
    </Card>
  )
}
