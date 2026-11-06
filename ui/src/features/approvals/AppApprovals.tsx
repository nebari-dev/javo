import { useId } from 'react'
import type { Plan } from '../../api'
import { useMe } from '../../lib/queries'
import { Actions, AgentMark, Chips, Result, useDecision, useToast } from './parts'
import { useInbox } from './useInbox'

/** One request on its app's page: the question with the app as "this app", its chips, the buttons inline. */
function AppBanner({ plan, onDecided, toast }: { plan: Plan; onDecided: (p: Plan) => void; toast: (t: string) => void }) {
  const d = useDecision(plan, { onDecided, toast })
  const titleId = useId()
  return (
    <div className={d.decided ? 'card bnr decided' : 'card bnr'} role="article" aria-labelledby={titleId}>
      <AgentMark plan={plan} />
      <div className="bnr-text">
        <h2 id={titleId}>{d.r.banner}</h2>
        {d.decided ? <Result d={d} /> : <Chips items={d.r.chips} />}
      </div>
      {!d.decided && (
        <div className="bar">
          <Actions d={d} />
        </div>
      )}
      {d.confirm}
    </div>
  )
}

/** The requests waiting for the person on one of their apps, on that app's page; nothing when none. */
export function AppApprovals({ name }: { name: string }) {
  const me = useMe().data
  const { plans, onDecided } = useInbox((p) => p.planned.target === name && (!p.planned.owner || p.planned.owner === me?.id))
  const toast = useToast()
  if (plans.length === 0) return null
  return (
    <section className="bnr-stack" aria-label="Approvals for this app">
      {plans.map((p) => (
        <AppBanner key={p.id} plan={p} onDecided={onDecided} toast={toast.show} />
      ))}
      {toast.node}
    </section>
  )
}
