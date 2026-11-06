import { Navigate, useRoutes } from 'react-router'
import { AppShell } from './AppShell'
import { features } from '../features'
import { AppDetail } from '../features/apps/AppDetail'
import { Apps } from '../features/apps/Apps'
import { Home } from '../features/home/Home'
import { Login } from '../features/auth/Login'
import { NewApp } from '../features/apps/NewApp'
import { PlanPage } from '../features/ai/PlanPage'
import { SessionGuard } from './SessionGuard'
import { Secrets } from '../features/settings/Secrets'
import { ApiTokens, Settings } from '../features/settings/Settings'
import { AdminAI } from '../features/settings/admin/AdminAI'
import { AdminAgents } from '../features/settings/admin/AdminAgents'
import { AdminApps } from '../features/settings/admin/AdminApps'
import { Backups } from '../features/settings/admin/Backups'
import { Storage } from '../features/settings/admin/Storage'
import { Usage } from '../features/settings/admin/Usage'
import { Templates } from '../features/templates/Templates'

export function AppRoutes() {
  return useRoutes([
    { path: 'login', element: <Login /> },
    {
      element: (
        <>
          <SessionGuard />
          <AppShell />
        </>
      ),
      children: [
        { index: true, element: <Home /> },
        { path: 'apps', element: <Apps /> },
        { path: 'apps/new', element: <NewApp /> },
        { path: 'apps/:name', element: <AppDetail /> },
        { path: 'templates', element: <Templates /> },
        {
          path: 'settings',
          element: <Settings />,
          children: [
            { index: true, element: <ApiTokens /> },
            { path: 'secrets', element: <Secrets /> },
            { path: 'apps', element: <AdminApps /> },
            { path: 'usage', element: <Usage /> },
            { path: 'backups', element: <Backups /> },
            { path: 'storage', element: <Storage /> },
            { path: 'ai', element: <AdminAI /> },
            { path: 'agents', element: <AdminAgents /> },
          ],
        },
        { path: 'plans/:id', element: <PlanPage /> },
        { path: 'admin/storage', element: <Navigate to="/settings/storage" replace /> },
        { path: 'shared', element: <Navigate to="/apps?show=shared" replace /> },
        ...features.flatMap((f) => f.routes),
      ],
    },
  ])
}
