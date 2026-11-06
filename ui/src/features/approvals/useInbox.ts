import { useState } from 'react'
import type { Plan } from '../../api'
import { usePendingPlans } from './queries'

/**
 * The plans to show: the pending ones, and those whose approval failed in
 * this page, kept so the reason stays on screen after they leave the list.
 */
export function useInbox(filter: (p: Plan) => boolean = () => true) {
  const q = usePendingPlans()
  const [failed, setFailed] = useState<Plan[]>([])
  const pending = (q.data ?? []).filter(filter)
  const kept = failed.filter((f) => filter(f) && !pending.some((p) => p.id === f.id))
  const onFailed = (p: Plan) => setFailed((all) => [...all.filter((x) => x.id !== p.id), p])
  return { q, plans: [...pending, ...kept], onFailed }
}
