import { FolderOpen } from 'lucide-react'
import { lazy, Suspense } from 'react'
import type { FeatureModule } from '..'

// The pages load on first visit, so they add nothing to the main bundle.
const FilesPage = lazy(() => import('./FilesPage').then((m) => ({ default: m.FilesPage })))
const FileView = lazy(() => import('./FileView').then((m) => ({ default: m.FileView })))

export const filesFeature: FeatureModule = {
  id: 'files',
  routes: [
    {
      path: 'files',
      element: (
        <Suspense>
          <FilesPage />
        </Suspense>
      ),
    },
    {
      path: 'files/view',
      element: (
        <Suspense>
          <FileView />
        </Suspense>
      ),
    },
  ],
  nav: { to: '/files', label: 'Files', icon: FolderOpen },
}
