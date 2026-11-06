import { Card } from '../../components/Card'
import { problemText } from '../../lib/http'
import { ApprovalCard } from './ApprovalCard'
import { useInbox } from './useInbox'

/** The person's approvals inbox: every change an agent, a rule or a search proposed, waiting for them. */
export function Approvals() {
  const { q, plans, onFailed } = useInbox()
  return (
    <>
      <h1>Approvals</h1>
      <p className="sub">Changes waiting for you. Nothing changes until you approve it.</p>
      <div className="section stack">
        {q.isError && <Card className="empty">{problemText(q.error)}</Card>}
        {q.isPending && <div className="muted">Loading</div>}
        {q.isSuccess && plans.length === 0 && <Card className="empty">Nothing is waiting for you.</Card>}
        {plans.map((p) => (
          <ApprovalCard key={p.id} plan={p} onFailed={onFailed} />
        ))}
      </div>
    </>
  )
}
