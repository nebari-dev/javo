import { useQuery } from '@tanstack/react-query'
import { listPlans } from '../../api'

/** The query key of the person's pending plans; deciding a plan reads it again. */
export const PENDING_PLANS = ['plans', 'pending'] as const

/** The person's plans waiting for a decision, read again every 30 seconds for the sidebar count. */
export function usePendingPlans() {
  return useQuery({
    queryKey: PENDING_PLANS,
    queryFn: async () => (await listPlans({ throwOnError: true })).data ?? [],
    refetchInterval: 30_000,
  })
}

/** How many plans wait for the person; 0 while unknown. */
export function usePendingCount(): number {
  return usePendingPlans().data?.length ?? 0
}
