import type { SharedApp, Workspace } from '../../api'

/** One entry of an apps list: one of the user's own apps, or an app shared with them. */
export type ListedApp = { mine: Workspace; shared?: never } | { shared: SharedApp; mine?: never }

export const listedKey = (a: ListedApp) => (a.mine ? a.mine.name : `${a.shared.owner.id}/${a.shared.name}`)

/**
 * The user's apps and the shared ones as one list, newest first. Both arrive
 * newest first from the API, so they are merged in that order; an entry
 * without a creation date keeps its place in its own list.
 */
export function newestFirst(mine: Workspace[], shared: SharedApp[]): ListedApp[] {
  const out: ListedApp[] = []
  let i = 0
  let j = 0
  while (i < mine.length || j < shared.length) {
    const m = mine[i]
    const s = shared[j]
    const sharedNext = !m || (s && !!s.createdAt && !!m.createdAt && s.createdAt > m.createdAt)
    if (sharedNext) out.push({ shared: shared[j++] })
    else out.push({ mine: mine[i++] })
  }
  return out
}
