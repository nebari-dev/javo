import { Link, useParams } from 'react-router'
import { Card } from '../../components/Card'
import { problemText } from '../../lib/http'
import { usePlan } from '../ai/ai'
import { ApprovalCard } from './ApprovalCard'
import { useToast } from './parts'
import { usePendingCount } from './queries'

/** The page an agent's approve link opens: the one request, as the same card the inbox shows. */
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
      <h1>Approvals</h1>
      <p className="sub">
        <Link to="/approvals">{waiting > 0 ? `See all approvals (${waiting})` : 'See all approvals'}</Link>
      </p>
      <div className="section rq-stack">
        <ApprovalCard plan={q.data} toast={toast.show} />
      </div>
      {toast.node}
    </>
  )
}
