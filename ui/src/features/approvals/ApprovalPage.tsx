import { Link, useParams } from 'react-router'
import { Card } from '../../components/Card'
import { problemText } from '../../lib/http'
import { usePlan } from '../ai/ai'
import { agentOf, minutesLeft, outcome } from './copy'
import { Actions, AgentMark, Result, useDecision, useToast } from './parts'
import { usePendingCount } from './queries'
import type { Plan } from '../../api'

/** The question, each consequence with a line of why, and the two buttons stacked. */
function Sheet({ plan, toast }: { plan: Plan; toast: (t: string) => void }) {
  const d = useDecision(plan, { toast })
  const agent = agentOf(plan)
  // A request opened after it was decided says what became of it.
  const earlier = plan.state !== 'pending' ? outcome(plan, d.r, true) : ''
  const left = minutesLeft(plan)
  return (
    <Card className="sheet">
      <div className="asker">
        <AgentMark plan={plan} />
        <span>
          <b>{agent.name}</b>
          {agent.where ? `, ${agent.where.replace(/^in /, 'working in ')}, asks:` : ' asks:'}
        </span>
      </div>
      <h1>{d.r.question}</h1>
      {earlier ? (
        <p className="rq-done">
          <span>{earlier}</span>
        </p>
      ) : d.decided ? (
        <Result d={d} />
      ) : (
        <>
          <ul className="cons" aria-label="What happens">
            {d.r.chips.map((c) => (
              <li key={c.text} className={c.tone}>
                <i>
                  <c.icon size={16} aria-hidden="true" />
                </i>
                <div>
                  <b>{c.text}</b>
                  <span>{c.detail}</span>
                </div>
              </li>
            ))}
          </ul>
          <div className="bar">
            <Actions d={d} wide />
          </div>
          <p className="foot">{left > 0 ? `Expires in ${left} min. If you don't decide, nothing changes.` : "It has expired. Nothing changes."}</p>
        </>
      )}
      {d.confirm}
    </Card>
  )
}

/** The page an agent's approve link opens: one request, decided right here. */
export function ApprovalPage() {
  const { id = '' } = useParams()
  const q = usePlan(id)
  const waiting = usePendingCount()
  const toast = useToast()
  if (q.isError) {
    return (
      <Card className="empty">
        {problemText(q.error)} <Link to="/approvals">See all approvals</Link>
      </Card>
    )
  }
  if (!q.data) return <div className="muted">Loading</div>
  return (
    <>
      <Sheet plan={q.data} toast={toast.show} />
      <p className="allreq">
        <Link to="/approvals">{waiting > 0 ? `See all approvals (${waiting})` : 'See all approvals'}</Link>
      </p>
      {toast.node}
    </>
  )
}
