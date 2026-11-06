import { ClipboardCheck } from 'lucide-react'
import { useId } from 'react'
import { useMe } from '../../lib/queries'
import { ApprovalCard } from './ApprovalCard'
import { useInbox } from './useInbox'

/** The plans waiting for the person on one of their apps, on that app's page; nothing when none. */
export function AppApprovals({ name }: { name: string }) {
  const me = useMe().data
  const { plans, onFailed } = useInbox((p) => p.planned.target === name && (!p.planned.owner || p.planned.owner === me?.id))
  const id = useId()
  if (plans.length === 0) return null
  return (
    <section className="app-approvals" aria-labelledby={id}>
      <div className="label" id={id}>
        <ClipboardCheck size={15} aria-hidden="true" />
        Waiting for your approval
      </div>
      {plans.map((p) => (
        <ApprovalCard key={p.id} plan={p} onFailed={onFailed} />
      ))}
    </section>
  )
}
