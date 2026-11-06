import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  startWorkspace,
  adminBackUpEverythingNow,
  adminGetAiSettings,
  adminGetAgentSettings,
  adminSetAgentSettings,
  adminResetAiSettings,
  adminSetAiSettings,
  adminBackUpNow,
  adminListApps,
  adminListBackups,
  adminListRestores,
  adminPreviewRestore,
  adminReleaseRestore,
  adminRestore,
  adminRestoreNow,
  adminUsage,
  createJob,
  deleteWorkspace,
  stopWorkspace,
  type AdminSetAiSettingsData,
  type RestoreScope,
  type RunRequestBody,
} from '../../../api'
import { POLL_MS, SHARED_KEY, useMe } from '../../../lib/queries'

const keys = {
  apps: ['admin', 'apps'] as const,
  usage: (by: string, days: number) => ['admin', 'usage', by, days] as const,
  backups: ['admin', 'backups'] as const,
  restores: ['admin', 'restores'] as const,
  ai: ['admin', 'ai'] as const,
  agents: ['admin', 'agents'] as const,
}

/** Whether the signed-in person is a platform admin (GET /api/me roles). */
export function useIsAdmin(): boolean {
  return !!useMe().data?.roles?.includes('platform-admin')
}

/** Every person's apps; polled while any is changing. */
export function useAdminApps() {
  return useQuery({
    queryKey: keys.apps,
    queryFn: async () => (await adminListApps({ throwOnError: true })).data.items ?? [],
    refetchInterval: (q) => (q.state.data?.some((a) => ['Starting', 'Pending', 'Stopping'].includes(a.app.phase)) ? POLL_MS : false),
  })
}

type OwnedApp = { name: string; owner: string }

/** A change to someone else's app; the lists that show it, All apps and the shared apps, are asked again. */
function useOthersAppChange<T>(fn: (app: OwnedApp) => Promise<T>) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.apps })
      void qc.invalidateQueries({ queryKey: SHARED_KEY })
    },
  })
}

/** Stop someone's app; the owner is told an admin stopped it. */
export function useAdminStop() {
  return useOthersAppChange(async ({ name, owner }) => (await stopWorkspace({ path: { name }, query: { owner }, throwOnError: true })).data)
}

export function useAdminStart() {
  return useOthersAppChange(async ({ name, owner }) => (await startWorkspace({ path: { name }, query: { owner }, throwOnError: true })).data)
}

/** Delete someone's app; it goes for its owner and everyone it is shared with. */
export function useAdminDelete() {
  return useOthersAppChange(async ({ name, owner }) => {
    await deleteWorkspace({ path: { name }, query: { owner }, throwOnError: true })
  })
}

export function useUsage(by: 'person' | 'group', days: number) {
  return useQuery({
    queryKey: keys.usage(by, days),
    queryFn: async () => (await adminUsage({ query: { by, days }, throwOnError: true })).data,
  })
}

/** Whether a run of the backup or restore job has not ended yet. */
export function runGoing(outcome?: string): boolean {
  return outcome === '' || outcome === 'Running'
}

/** The Backups page; polled while a full backup or a restore runs. */
export function useBackups() {
  return useQuery({
    queryKey: keys.backups,
    queryFn: async () => (await adminListBackups({ throwOnError: true })).data,
    refetchInterval: (q) => {
      const b = q.state.data?.bucket
      return b && (runGoing(b.lastRun?.outcome) || runGoing(b.lastRestore?.outcome)) ? POLL_MS : false
    },
  })
}

/** Run the backup job now: one full backup of the database, every home folder and every shared folder. */
export function useBackUpEverythingNow() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => (await adminBackUpEverythingNow({ body: {}, throwOnError: true })).data,
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.backups }),
  })
}

/** Run the restore job now: one full backup, for everyone or one person. */
export function useRestoreNow() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: RunRequestBody) => (await adminRestoreNow({ body, throwOnError: true })).data,
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.backups })
      void qc.invalidateQueries({ queryKey: keys.restores })
    },
  })
}

/**
 * Set up backups: the backup job (every night at 01:00 in this browser's
 * time zone) and the restore job, made through the ordinary jobs API as New
 * job makes any job, from the built-in template "backup".
 */
export function useSetUpBackups() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      const timeZone = Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
      await createJob({
        body: { displayName: 'Backup', template: 'backup', engine: 'command', command: '/javo backup run', schedule: { cron: '0 1 * * *', timeZone } },
        throwOnError: true,
      })
      await createJob({
        body: {
          displayName: 'Restore',
          template: 'backup',
          engine: 'command',
          command: '/javo backup restore-run',
          parameters: [
            { name: 'from', type: 'string', default: '' },
            { name: 'person', type: 'string', default: '' },
            { name: 'mode', type: 'string', default: 'replace' },
          ],
        },
        throwOnError: true,
      })
    },
    onSettled: () => void qc.invalidateQueries({ queryKey: keys.backups }),
  })
}

export function useBackUpNow() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => (await adminBackUpNow({ throwOnError: true })).data,
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.backups }),
  })
}

/** What restoring apps from a backup would do; changes nothing. */
export function useRestorePreview() {
  return useMutation({
    mutationFn: async ({ name, scope }: { name: string; scope: RestoreScope }) =>
      (await adminPreviewRestore({ path: { name }, body: scope, throwOnError: true })).data.rows ?? [],
  })
}

/** Bring back the apps the preview says come back; they wait for a release. */
export function useRestore() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ name, scope }: { name: string; scope: RestoreScope }) =>
      (await adminRestore({ path: { name }, body: scope, throwOnError: true })).data.rows ?? [],
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.restores }),
  })
}

/** Restores whose apps wait for a release; polled while some are scheduled. */
export function useRestores() {
  return useQuery({
    queryKey: keys.restores,
    queryFn: async () => (await adminListRestores({ throwOnError: true })).data.items ?? [],
    refetchInterval: (q) => (q.state.data?.some((r) => r.scheduled > 0) ? POLL_MS : false),
  })
}

export function useRelease() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ name, mode, perMinute }: { name: string; mode: 'start' | 'stopped'; perMinute?: number }) =>
      (await adminReleaseRestore({ path: { name }, body: mode === 'start' ? { mode, perMinute } : { mode }, throwOnError: true })).data,
    onSuccess: () => void qc.invalidateQueries({ queryKey: keys.restores }),
  })
}

/** The AI settings in force; asked only for a platform admin. */
/** Whether owners may let their coding agents act without asking. */
export function useAgentSettings(enabled: boolean) {
  return useQuery({ queryKey: keys.agents, enabled, queryFn: async () => (await adminGetAgentSettings({ throwOnError: true })).data })
}

/** Allow or refuse it; refusing ends it on every agent at once, so the apps are asked again. */
export function useSetAgentSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (allowTrusted: boolean) => (await adminSetAgentSettings({ body: { allowTrusted }, throwOnError: true })).data,
    onSuccess: (s) => {
      qc.setQueryData(keys.agents, s)
      void qc.invalidateQueries({ queryKey: ['workspaces'] })
    },
  })
}

export function useAISettings(enabled: boolean) {
  return useQuery({ queryKey: keys.ai, enabled, queryFn: async () => (await adminGetAiSettings({ throwOnError: true })).data })
}

/**
 * Save or reset the AI settings. Every AI answer and the coding agents'
 * model follow at once, so what the UI knows of them is asked again.
 */
export function useSaveAISettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: AdminSetAiSettingsData['body']) => (await adminSetAiSettings({ body, throwOnError: true })).data,
    onSuccess: (s) => {
      qc.setQueryData(keys.ai, s)
      void qc.invalidateQueries({ queryKey: ['ai'] })
    },
  })
}

export function useResetAISettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      await adminResetAiSettings({ throwOnError: true })
    },
    onSuccess: () => {
      void qc.invalidateQueries({ queryKey: keys.ai })
      void qc.invalidateQueries({ queryKey: ['ai'] })
    },
  })
}
