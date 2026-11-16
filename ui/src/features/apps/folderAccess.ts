import type { FolderGrant, FoldersView, LockPlace } from '../../api'

export type Mode = FolderGrant['mode']

/** How many folders and files a person may add to one agent; the API refuses more. */
export const MAX_GRANTS = 16

/** True when path is dir or inside it. */
export const within = (dir: string, path: string) => path === dir || path.startsWith(`${dir}/`)

/** The project as a path of the home, or undefined for one outside it (an app from a repository). */
export const projectPath = (view: FoldersView) => (view.project.startsWith('~/') ? view.project.slice(2) : undefined)

/**
 * How the API holds a path of the home: none for an ordinary one, else the
 * longest place it is, or is in (a place of rule around holds only itself),
 * else the one the API sends for every other hidden name.
 */
export function placeOf(view: FoldersView, path: string): LockPlace | undefined {
  if (!path.startsWith(view.hidden.path)) return undefined
  let best: LockPlace | undefined
  for (const p of view.places ?? []) {
    const holds = path === p.path || (p.rule !== 'around' && path.startsWith(`${p.path}/`))
    if (holds && (!best || p.path.length > best.path.length)) best = p
  }
  return best ?? view.hidden
}

/**
 * What one row of the home is to the agent:
 * project (always open), fixed (a place every agent has open), shut (a place
 * no agent opens), added (ticked by the person), included (inside the project
 * or something ticked), or free (closed until ticked; with a place, only
 * after its warning).
 */
export type Row =
  | { is: 'project'; mode: 'write' }
  | { is: 'fixed'; mode: Mode; place: LockPlace }
  | { is: 'shut'; place: LockPlace }
  | { is: 'added'; mode: Mode; place?: LockPlace }
  | { is: 'included'; mode: Mode; from: string; place?: LockPlace }
  | { is: 'free'; place?: LockPlace }

export function rowOf(view: FoldersView, grants: FolderGrant[], path: string): Row {
  const project = projectPath(view)
  if (path === project) return { is: 'project', mode: 'write' }
  const place = placeOf(view, path)
  const own = grants.find((g) => g.path === path)
  if (own) return { is: 'added', mode: own.mode, place }
  if (place?.rule === 'open') return { is: 'fixed', mode: place.mode ?? 'read', place }
  if (place && place.rule !== 'risky') return { is: 'shut', place }
  // The widest of what it is inside; the outermost of those when they tie.
  const around = [...(project ? [{ path: project, mode: 'write' as Mode }] : []), ...grants]
    .filter((g) => within(g.path, path))
    .sort((a, b) => a.path.length - b.path.length)
  const from = around.find((g) => g.mode === 'write') ?? around[0]
  if (from) return { is: 'included', mode: from.mode, from: from.path, place }
  return { is: 'free', place }
}

/** The mode a row is open to, or undefined for a closed one. */
export const openTo = (r: Row): Mode | undefined => (r.is === 'shut' || r.is === 'free' ? undefined : r.mode)

/**
 * grants with path added to read; what was added inside it to read only is
 * dropped, as the folder now covers it. A place that asks first is added as
 * allowed (risky).
 */
export function withGrant(grants: FolderGrant[], path: string, risky: boolean): FolderGrant[] {
  const kept = grants.filter((g) => !(g.mode === 'read' && g.path.startsWith(`${path}/`)))
  return [...kept, { path, mode: 'read', ...(risky ? { risky: true } : {}) }]
}

/** grants without those inside the project, which is open already and which the API would refuse. */
export function outsideProject(view: FoldersView, grants: FolderGrant[]): FolderGrant[] {
  const project = projectPath(view)
  return project ? grants.filter((g) => !within(project, g.path)) : grants
}

/** Whether two lists grant the same; the API keeps risky only where it applies, so it is not compared. */
export function sameGrants(a: FolderGrant[], b: FolderGrant[]): boolean {
  const key = (g: FolderGrant) => `${g.mode} ${g.path}`
  const have = new Set(a.map(key))
  return a.length === b.length && b.every((g) => have.has(key(g)))
}
