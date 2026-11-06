import { useState } from 'react'
import type { Plan } from '../../api'
import { usePendingPlans } from './queries'

/**
 * The plans to show: the pending ones, and those decided in this page, kept
 * so the result stays on screen after they leave the list. waiting counts
 * the ones still to decide.
 */
export function useInbox(filter: (p: Plan) => boolean = () => true) {
  const q = usePendingPlans()
  const [decided, setDecided] = useState<Plan[]>([])
  const pending = (q.data ?? []).filter(filter)
  const kept = decided.filter((f) => filter(f) && !pending.some((p) => p.id === f.id))
  const onDecided = (p: Plan) => setDecided((all) => [...all.filter((x) => x.id !== p.id), p])
  const waiting = pending.filter((p) => !decided.some((x) => x.id === p.id)).length
  // Oldest first, as the list comes, so a decided card stays where it was.
  const plans = [...pending, ...kept].sort((a, b) => Date.parse(a.createdAt) - Date.parse(b.createdAt))
  return { q, plans, onDecided, waiting }
}
