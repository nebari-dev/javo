import { ClipboardCheck } from 'lucide-react'
import type { FeatureModule } from '..'
import { PlanPage } from '../ai/PlanPage'
import { Approvals } from './Approvals'
import { usePendingCount } from './queries'

export const approvalsFeature: FeatureModule = {
  id: 'approvals',
  routes: [
    { path: 'approvals', element: <Approvals /> },
    { path: 'approvals/:id', element: <PlanPage /> },
  ],
  nav: { to: '/approvals', label: 'Approvals', icon: ClipboardCheck, useCount: usePendingCount },
}
