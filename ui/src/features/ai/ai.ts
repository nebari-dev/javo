import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  aiCommand,
  aiFeedback,
  approvePlan,
  askAdmin,
  buildWithAgent,
  deleteAgentSettings,
  getAgentStatus,
  setAgentSettings,
  suggestAgentRequest,
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
import { PENDING_PLANS } from '../approvals/queries'

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
    // Decided or failed, it is no longer waiting: the inbox and its count read again.
    onSettled: () => qc.invalidateQueries({ queryKey: PENDING_PLANS }),
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

const agentKey = ['ai', 'agent'] as const

/** Where the person's coding agents get their model, and whether they may start one. */
export function useAgentStatus() {
  return useQuery({ queryKey: agentKey, queryFn: async () => (await getAgentStatus({ throwOnError: true })).data, staleTime: 60_000 })
}

/** Stores the person's own agent model and key (the key is write-only). */
export function useSaveAgentSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: { baseURL: string; model: string; apiKey?: string }) => (await setAgentSettings({ body, throwOnError: true })).data,
    onSuccess: (s) => qc.setQueryData(agentKey, s),
  })
}

export function useDeleteAgentSettings() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async () => {
      await deleteAgentSettings({ throwOnError: true })
    },
    onSuccess: () => qc.invalidateQueries({ queryKey: agentKey }),
  })
}

/** Starts a coding-agent app on the request. */
/** The agent's name in "Build with Pi": its template name with a capital first letter. */
export function agentLabel(template: string): string {
  return template.charAt(0).toUpperCase() + template.slice(1)
}

export function useBuildWithAgent() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (request: string) => (await buildWithAgent({ body: { request }, throwOnError: true })).data,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['workspaces'] }),
  })
}

/** Fewer characters than this are not worth suggesting for. */
const SUGGEST_MIN = 3
/** How long typing pauses before suggestions are asked for. */
const SUGGEST_PAUSE_MS = 400

/**
 * Whole descriptions that complete text, asked for once typing pauses. Empty
 * while off, under three characters, or when the model has none; a failure
 * shows nothing either.
 */
export function useAgentSuggestions(text: string, enabled: boolean): string[] {
  const [settled, setSettled] = useState('')
  useEffect(() => {
    const t = setTimeout(() => setSettled(text.trim()), SUGGEST_PAUSE_MS)
    return () => clearTimeout(t)
  }, [text])
  const on = enabled && settled.length >= SUGGEST_MIN && settled === text.trim()
  const q = useQuery({
    queryKey: ['ai', 'agent', 'suggest', settled],
    enabled: on,
    retry: false,
    staleTime: 5 * 60_000,
    queryFn: async ({ signal }) => (await suggestAgentRequest({ body: { text: settled }, signal, throwOnError: true })).data.suggestions,
  })
  return on ? (q.data ?? []) : []
}
