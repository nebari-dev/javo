import { ClipboardCheck } from 'lucide-react'
import { Link, useParams } from 'react-router'
import { usePlan } from './ai'
import { Card } from '../../components/Card'
import { PlanReview } from './Plan'
import { problemText } from '../../lib/http'

/** Who proposed a plan, in words. */
function requestedBy(source: string): string {
  if (source.startsWith('mcp:')) return `Requested by ${source.slice(4)}`
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
            {requestedBy(p.source)}
          </div>
          <PlanReview plan={p} applyLabel="Approve" cancelLabel="Reject" />
        </Card>
      </div>
    </>
  )
}
