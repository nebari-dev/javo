import { ChevronDown } from 'lucide-react'
import { useId, useState } from 'react'
import type { Plan } from '../../api'
import { Card } from '../../components/Card'
import { Diff } from '../ai/Plan'
import { agentOf, askedLine } from './copy'
import { Actions, AgentMark, Chips, Result, Timer, useDecision } from './parts'

/**
 * One request in the inbox, as a question: who asked on the left; the
 * question, one line on what happens, its consequences as chips, and the
 * buttons on the right. The technical details fold under Details.
 */
export function ApprovalCard({ plan, onDecided, toast }: { plan: Plan; onDecided?: (p: Plan) => void; toast?: (t: string) => void }) {
  const d = useDecision(plan, { onDecided, toast })
  const [open, setOpen] = useState(false)
  const detailsId = useId()
  const titleId = useId()
  const agent = agentOf(plan)
  return (
    <Card className={d.decided ? 'rq decided' : 'rq'} role="article" aria-labelledby={titleId}>
      <div className="who">
        <AgentMark plan={plan} />
        <b>{agent.name}</b>
        <span>{askedLine(plan)}</span>
      </div>
      <div className="what">
        <div className="what-top">
          <h2 id={titleId}>{d.r.question}</h2>
          {!d.decided && <Timer plan={plan} />}
        </div>
        {d.decided ? (
          <Result d={d} />
        ) : (
          <>
            {d.r.say && <p className="say">{d.r.say}</p>}
            <Chips items={d.r.chips} />
            <div className="bar">
              <Actions d={d} />
              <span className="grow" />
              <button type="button" className="det-btn" aria-expanded={open} aria-controls={detailsId} onClick={() => setOpen(!open)}>
                Details
                <ChevronDown size={14} aria-hidden="true" />
              </button>
            </div>
            {open && (
              <div className="det" id={detailsId} role="group" aria-label="Details">
                <dl>
                  {d.r.details.map((row) => (
                    <div key={row.label}>
                      <dt>{row.label}</dt>
                      <dd>{row.value}</dd>
                    </div>
                  ))}
                </dl>
                <Diff changes={plan.planned.changes} />
              </div>
            )}
          </>
        )}
      </div>
      {d.confirm}
    </Card>
  )
}
