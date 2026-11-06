import { CircleAlert, CircleCheck, Power, ShieldCheck } from 'lucide-react'
import { Card } from '../../components/Card'
import { problemText } from '../../lib/http'
import { ApprovalCard } from './ApprovalCard'
import { Chips, useToast } from './parts'
import { useInbox } from './useInbox'

/** "2 requests need your decision. ..."; once every one shown is decided, says so. */
function helper(waiting: number): string {
  if (waiting === 0) return 'All decided. Nothing else needs you.'
  const n = waiting === 1 ? '1 request needs' : `${waiting} requests need`
  return `${n} your decision. Nothing happens until you decide.`
}

/** Nothing waits: what lands here, and the chips each request carries. */
function Empty() {
  return (
    <Card className="rq-empty">
      <span className="agent" aria-hidden="true">
        <ShieldCheck size={22} />
      </span>
      <h2>No approvals waiting</h2>
      <p>Agents ask here before they use your secrets or start, stop, restart, resize or delete an app.</p>
      <div className="legend">
        <p>Each request shows what it does to you:</p>
        <Chips
          label="Kinds of consequence"
          items={[
            { tone: 'ok', icon: CircleCheck, text: 'Safe or reversible', detail: '' },
            { tone: 'warn', icon: Power, text: 'Interrupts an app', detail: '' },
            { tone: 'err', icon: CircleAlert, text: "Can't be undone", detail: '' },
          ]}
        />
      </div>
    </Card>
  )
}

/** The person's approvals inbox: every change an agent, a rule or a search proposed, waiting for them. */
export function Approvals() {
  const { q, plans, onDecided, waiting } = useInbox()
  const toast = useToast()
  return (
    <>
      <h1>Approvals</h1>
      {plans.length > 0 && <p className="sub">{helper(waiting)}</p>}
      <div className="section rq-stack">
        {q.isError && <Card className="empty">{problemText(q.error)}</Card>}
        {q.isPending && <div className="muted">Loading</div>}
        {q.isSuccess && plans.length === 0 && <Empty />}
        {plans.map((p) => (
          <ApprovalCard key={p.id} plan={p} onDecided={onDecided} toast={toast.show} />
        ))}
      </div>
      {toast.node}
    </>
  )
}
