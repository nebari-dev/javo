import { ClipboardCheck } from 'lucide-react'
import { Link, useParams } from 'react-router'
import { usePlan } from './ai'
import { Card } from '../../components/Card'
import { PlanReview } from './Plan'
import { problemText } from '../../lib/http'
import type { Plan } from '../../api'

/**
 * Which credential proposed a plan, as the gateway knows it; empty for the
 * person's own browser session.
 */
function credential(by: Plan['requestedBy'] | undefined): string {
  if (by?.app) return `the token of your app ${by.app}`
  if (by?.job) return 'the token of one of your jobs'
  if (by?.token) return `your API token ${by.token}`
  return ''
}

/**
 * Who proposed a plan, in words. An agent names itself, so its name is
 * given as what it calls itself, after the credential it used.
 */
export function requestedBy(source: string, by: Plan['requestedBy'] | undefined): string {
  if (source.startsWith('mcp:')) {
    const agent = `an agent that calls itself ${source.slice(4)}`
    const cred = credential(by)
    return cred ? `Requested with ${cred}, by ${agent}` : `Requested by ${agent}`
  }
  if (source === 'palette') return 'Asked for in search'
  if (source === 'ai-diagnosis') return 'Suggested by the AI explanation'
  return 'Suggested by the failure diagnosis'
}

/** The page an agent's approve link opens: the proposed change, Approve or Reject. */
export function PlanPage() {
  const { id = '' } = useParams()
  const q = usePlan(id)
  if (q.isError) {
    return (
      <Card className="empty">
        {problemText(q.error)} <Link to="/apps">Back to Apps</Link>
      </Card>
    )
  }
  const p = q.data
  if (!p) return <div className="muted">Loading</div>
  return (
    <>
      <h1>Review a change</h1>
      <p className="sub">Nothing changes until you approve it here.</p>
      <div className="section">
        <Card className="pad plan-page">
          <div className="label">
            <ClipboardCheck size={15} aria-hidden="true" />
            {requestedBy(p.source, p.requestedBy)}
          </div>
          <PlanReview plan={p} applyLabel="Approve" cancelLabel="Reject" confirmDestructive />
        </Card>
      </div>
    </>
  )
}
