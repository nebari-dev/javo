import type { Step, Workspace } from '../../api'
import type { Status } from '../../components/Pill'

const PILL: Record<string, Status> = {
  Running: 'running',
  Starting: 'starting',
  Stopping: 'stopping',
  Pending: 'pending',
  Failed: 'failed',
  Stopped: 'stopped',
}

export function pillStatus(phase: string): Status {
  return PILL[phase] ?? 'pending'
}

// Catalog codes that report progress, not a problem.
const PROGRESS = new Set(['Starting', 'Ready', 'Stopped', 'StoppedIdle', 'StoppedByAdmin', 'RestoreHeld'])

/**
 * The step the user should hear about: the first step that is False for a
 * reason other than normal progress. None for a stopped app.
 */
export function problemStep(ws: Workspace): Step | undefined {
  if (ws.phase === 'Stopped') return undefined
  return (ws.steps ?? []).find((s) => s.status === 'False' && !PROGRESS.has(s.reason))
}

/**
 * Why a stopped app stopped, when the platform stopped it (for being idle):
 * the catalog's line from its first step, for example "Stopped after 60
 * minutes without use". None for an app a person stopped.
 */
export function stopNote(ws: Workspace): string | undefined {
  if (ws.phase !== 'Stopped') return undefined
  const step = ws.steps?.[0]
  if (step?.reason === 'StoppedIdle') return step.evidence?.[0]
  if (step?.reason === 'StoppedByAdmin') return step.title
  return undefined
}

/** The catalog step of a restored app waiting for an admin; none otherwise. */
export function heldStep(ws: Workspace): Step | undefined {
  const step = ws.steps?.[0]
  return step?.reason === 'RestoreHeld' ? step : undefined
}

/** The problem to show on an app's card or row: its step copy and a tone. */
export function problemNote(ws: Workspace): { title: string; tone: 'err' | 'warn' } | undefined {
  const step = problemStep(ws)
  return step && { title: step.title, tone: ws.phase === 'Failed' ? 'err' : 'warn' }
}

/**
 * The address to open an app at: its path on this host, or its http(s) URL on
 * the apps host or its own subdomain. Any other scheme is not followed.
 */
export function appLink(url: string): string | undefined {
  return url.startsWith('/u/') || /^https?:\/\//.test(url) ? url : undefined
}

/**
 * The step that says an admin's storage move holds the app: the app is
 * Stopped, nothing failed, and the user only has to wait.
 */
export function migratingStep(ws: Workspace): Step | undefined {
  if (ws.phase !== 'Stopped') return undefined
  return (ws.steps ?? []).find((s) => s.status === 'False' && s.reason === 'HomeMigrating')
}
