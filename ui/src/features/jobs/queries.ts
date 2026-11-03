import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import {
  cancelJobRun,
  createJob,
  getJob,
  getJobRunLog,
  listEngines,
  listJobs,
  pauseJob,
  previewSchedule,
  resumeJob,
  runJob,
  setJobEnv,
  setJobSize,
  updateJob,
  type EnvVar,
  type JobBody,
  type RunView,
} from '../../api'
import { useFollowedBody } from '../../lib/follow'
import { POLL_MS, SECRETS_KEY } from '../../lib/queries'
import type { SizeChoice } from '../../lib/sizes'

const keys = {
  jobs: ['jobs'] as const,
  job: (name: string) => ['jobs', name] as const,
  log: (name: string, run: string) => ['jobs', name, 'runs', run, 'log'] as const,
  engines: ['engines'] as const,
  preview: (cron: string, timeZone: string) => ['schedule-preview', cron, timeZone] as const,
}

export const isRunning = (run?: Pick<RunView, 'outcome'>): boolean => run?.outcome === 'Running'

export function useJobs() {
  return useQuery({
    queryKey: keys.jobs,
    queryFn: async () => (await listJobs({ throwOnError: true })).data.items ?? [],
    refetchInterval: (q) => (q.state.data?.some((j) => isRunning(j.lastRun)) ? POLL_MS : false),
  })
}

export function useJob(name: string) {
  return useQuery({
    queryKey: keys.job(name),
    queryFn: async () => (await getJob({ path: { name }, throwOnError: true })).data,
    refetchInterval: (q) => (q.state.data?.runs?.some(isRunning) ? POLL_MS : false),
  })
}

export function useEngines() {
  return useQuery({
    queryKey: keys.engines,
    queryFn: async () => (await listEngines({ throwOnError: true })).data.items ?? [],
  })
}

/** The next runs of a schedule as the cluster will run them; a 422 names the field at fault. */
export function useSchedulePreview(cron: string, timeZone: string) {
  return useQuery({
    queryKey: keys.preview(cron, timeZone),
    enabled: cron !== '' && timeZone !== '',
    retry: false,
    queryFn: async () => (await previewSchedule({ body: { cron, timeZone }, throwOnError: true })).data,
  })
}

/** A run's log, and whether it is the copy saved in the run folder (the run's pod is gone). */
interface RunLogText {
  text: string
  saved: boolean
}

/** A run's log: what it printed so far, or why it cannot be read. */
interface RunLogState {
  data?: RunLogText
  isPending: boolean
  error: Error | null
}

/**
 * The text of a run's log. While the run goes the server keeps the response
 * open and writes each line as it is printed, so the body is read as it
 * arrives (from the start of the log on each open); once the run ends the
 * whole log is read once more, so the last output is not lost.
 */
export function useRunLog(name: string, run: string, running: boolean): RunLogState {
  const live = useFollowedRunLog(name, run, running)
  const done = useQuery({
    queryKey: keys.log(name, run),
    enabled: !running,
    retry: false,
    queryFn: async (): Promise<RunLogText> => {
      const r = await getJobRunLog({ path: { name, run }, throwOnError: true })
      return { text: String(r.data ?? ''), saved: r.response.headers.get('X-Log-Source') === 'saved' }
    },
  })
  // Until the final read answers, the lines already followed stay on screen.
  if (running || (done.isPending && live !== undefined)) {
    return { data: live === undefined ? undefined : { text: live, saved: false }, isPending: live === undefined, error: null }
  }
  return { data: done.data, isPending: done.isPending, error: done.error }
}

/** The log of a running run as it is printed; undefined until its container has started. */
function useFollowedRunLog(name: string, run: string, running: boolean): string | undefined {
  // parseAs 'stream' makes the data the response body, which the generated type calls a string.
  const open = async (signal: AbortSignal) =>
    (await getJobRunLog({ path: { name, run }, parseAs: 'stream', signal, throwOnError: true })).data as unknown as ReadableStream<Uint8Array>
  return useFollowedBody(open, `${name}/${run}`, running, true)
}

/** Create a job, or replace the one called `name`. */
export function useSaveJob(name?: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (body: JobBody) =>
      name ? (await updateJob({ path: { name }, body, throwOnError: true })).data : (await createJob({ body, throwOnError: true })).data,
    onSuccess: (job) => {
      qc.setQueryData(keys.job(job.name), job)
      void qc.invalidateQueries({ queryKey: keys.jobs, exact: true })
      // A new job may use secrets, which their list names.
      if (job.env?.some((e) => e.secret)) void qc.invalidateQueries({ queryKey: SECRETS_KEY, exact: true })
    },
  })
}

/** Set the environment variables the runs of the job called `name` get, from its next run on. */
export function useSetJobEnv(name: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (env: EnvVar[]) => (await setJobEnv({ path: { name }, body: { env }, throwOnError: true })).data,
    onSuccess: (job) => {
      qc.setQueryData(keys.job(name), job)
      void qc.invalidateQueries({ queryKey: SECRETS_KEY, exact: true })
    },
  })
}

/** Change the size the runs of the job called `name` get, from its next run on. */
export function useSetJobSize(name: string) {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async (choice: SizeChoice) => (await setJobSize({ path: { name }, body: choice, throwOnError: true })).data,
    onSuccess: (job) => qc.setQueryData(keys.job(name), job),
  })
}

/** Start a run of the job called `name` (given when called, so a job just saved can be run); `parameters` override its saved inputs. */
export function useRunJob() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: async ({ name, parameters }: { name: string; parameters?: Record<string, string> }) =>
      (await runJob({ path: { name }, body: parameters ? { parameters } : {}, throwOnError: true })).data,
    onSettled: () => qc.invalidateQueries({ queryKey: keys.jobs }),
  })
}

/** Pause, resume and cancel: each answers with fresh state, so refetch the job and the list. */
export function useJobActions(name: string) {
  const qc = useQueryClient()
  const refresh = () => qc.invalidateQueries({ queryKey: keys.jobs })
  return {
    setPaused: useMutation({
      mutationFn: async (paused: boolean) =>
        (paused ? await pauseJob({ path: { name }, throwOnError: true }) : await resumeJob({ path: { name }, throwOnError: true })).data,
      onSuccess: (job) => {
        qc.setQueryData(keys.job(name), job)
        void refresh()
      },
    }),
    cancel: useMutation({
      mutationFn: async (run: string) => (await cancelJobRun({ path: { name, run }, throwOnError: true })).data,
      onSuccess: refresh,
    }),
  }
}
