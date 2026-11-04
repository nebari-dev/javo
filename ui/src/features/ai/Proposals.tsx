import { Wrench } from 'lucide-react'
import { useState } from 'react'
import { useCreatePlan } from './ai'
import type { Plan, ProposalView } from '../../api'
import { problemText } from '../../lib/http'
import { Button } from '../../components/Button'
import { PlanReview } from './Plan'

/**
 * One button per proposed fix. Pressing one shows the change for review
 * first; nothing happens until Apply.
 */
export function Proposals({ items, source }: { items: ProposalView[] | null; source: 'rules' | 'ai-diagnosis' }) {
  const create = useCreatePlan()
  const [plan, setPlan] = useState<Plan | null>(null)
  if (!items?.length) return null
  if (plan) return <PlanReview plan={plan} onDone={(p) => p.state === 'rejected' && setPlan(null)} />
  return (
    <div className="proposals">
      <div className="row proposal-buttons">
        {items.map((p) => (
          <Button
            key={`${p.action}:${p.target}`}
            size="sm"
            icon={Wrench}
            disabled={create.isPending}
            onClick={() => create.mutate({ proposal: { action: p.action, target: p.target, owner: p.owner, params: p.params }, source }, { onSuccess: setPlan })}
          >
            {p.summary}
          </Button>
        ))}
      </div>
      {create.isError && (
        <p className="plan-error" role="alert">
          {problemText(create.error)}
        </p>
      )}
    </div>
  )
}
