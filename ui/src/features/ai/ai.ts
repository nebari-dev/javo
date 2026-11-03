import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  aiCommand,
  aiFeedback,
  approvePlan,
  askAdmin,
  createPlan,
  draftWorkspace,
  explainDiagnosis,
  getAdminReport,
  getAiStatus,
  getDiagnosis,
  getPlan,
  rejectPlan,
  type AiFeatures,
  type Plan,
  type Proposal,
} from '../../api'

const keys = {
  status: ['ai', 'status'] as const,
  diagnosis: (name: string) => ['workspaces', name, 'diagnosis'] as const,
  plan: (id: string) => ['plans', id] as const,
  report: ['admin', 'report'] as const,
}

/** Which AI features are on. While loading, or when off, every feature reads as off. */
export function useAiStatus() {
  return useQuery({ queryKey: keys.status, queryFn: async () => (await getAiStatus({ throwOnError: true })).data, staleTime: 60_000 })
}

/** Whether one AI feature is on; false until the status is known. */
export function useAiFeature(feature: keyof AiFeatures): boolean {
  const s = useAiStatus().data
  return !!s?.enabled && !!s.features[feature]
}

/** The rules' diagnosis of a failed app. */
export function useDiagnosis(name: string, enabled: boolean) {
  return useQuery({
    queryKey: keys.diagnosis(name),
    enabled,
    queryFn: async () => (await getDiagnosis({ path: { name }, throwOnError: true })).data,
  })
}

/** Asks the model to explain the failure; preview only builds what would be sent. */
export function useExplain(name: string) {
  return useMutation({
    mutationFn: async (preview: boolean) => (await explainDiagnosis({ path: { name }, body: { preview }, throwOnError: true })).data,
  })
}

export function useAiFeedback() {
  return useMutation({
    mutationFn: async (body: { feature: 'explain' | 'draft' | 'palette' | 'admin'; ref: string; right: boolean }) => {
      await aiFeedback({ body, throwOnError: true })
    },
  })
}

/** Turns a proposal into a plan to review; nothing changes yet. */
export function useCreatePlan() {
  return useMutation({
    mutationFn: async (body: { proposal: Proposal; source: 'rules' | 'ai-diagnosis' | 'palette' }) =>
      (await createPlan({ body, throwOnError: true })).data,
  })
}

export function usePlan(id: string) {
  return useQuery({ queryKey: keys.plan(id), queryFn: async () => (await getPlan({ path: { id }, throwOnError: true })).data })
}

/** Approves or turns down a plan; approving makes the change, so the apps are read again. */
export function useDecidePlan() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ id, approve }: { id: string; approve: boolean }): Promise<Plan> =>
      approve ? (await approvePlan({ path: { id }, throwOnError: true })).data : (await rejectPlan({ path: { id }, throwOnError: true })).data,
    onSuccess: (p) => {
      qc.setQueryData(keys.plan(p.id), p)
      void qc.invalidateQueries({ queryKey: ['workspaces'] })
    },
  })
}

/** Asks in plain words for changes; the answer lists plans to approve. */
export function useAiCommand() {
  return useMutation({ mutationFn: async (text: string) => (await aiCommand({ body: { text }, throwOnError: true })).data })
}

/** Fills the new app form from a description; nothing is created. */
export function useDraft() {
  return useMutation({ mutationFn: async (text: string) => (await draftWorkspace({ body: { text, kind: 'app' }, throwOnError: true })).data })
}

export function useAdminReport(enabled: boolean) {
  return useQuery({ queryKey: keys.report, enabled, queryFn: async () => (await getAdminReport({ throwOnError: true })).data })
}

export function useAskAdmin() {
  return useMutation({ mutationFn: async (question: string) => (await askAdmin({ body: { question }, throwOnError: true })).data })
}
