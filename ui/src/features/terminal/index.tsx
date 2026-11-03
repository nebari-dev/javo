import { SquareTerminal } from 'lucide-react'
import { lazy, Suspense } from 'react'
import type { FeatureModule } from '..'

const TerminalPage = lazy(() => import('./TerminalPage').then((m) => ({ default: m.TerminalPage })))

/** A sidebar entry that opens the person's own terminal app (the Terminal template). */
export const terminalFeature: FeatureModule = {
  id: 'terminal',
  routes: [
    {
      path: 'terminal',
      element: (
        <Suspense>
          <TerminalPage />
        </Suspense>
      ),
    },
  ],
  nav: { to: '/terminal', label: 'Terminal', icon: SquareTerminal },
}
