import { listFiles, warmFiles, type FileEntry, type FileListing } from '../api'
import { ApiError, type Problem } from './http'

export type HomeEntry = FileEntry
type HomeFolder = Omit<FileListing, 'entries'> & { entries: FileEntry[] }

/** The place id of the user's home, as GET /api/files names it. */
export const HOME_PLACE = 'home'

/** The place id of a group folder, as GET /api/files names it. */
export const groupPlace = (dir: string) => `group:${dir}`

/**
 * One read of a folder: its listing (possibly the gateway's last one, marked
 * stale, while a fresh one is fetched), or the reason the home folder is
 * still opening. Opening is an answer, not a failure, so a poll that gets it
 * again changes nothing on screen.
 */
type HomeFolderRead = { kind: 'listed'; folder: HomeFolder } | { kind: 'opening'; reason: Problem }

/**
 * Lists one folder of a place: the signed-in user's home (the default) or a
 * group folder ("" is the place itself).
 */
export async function readHomeFolder(path: string, place: string = HOME_PLACE): Promise<HomeFolderRead> {
  try {
    const query = place === HOME_PLACE ? { path } : { path, place }
    const { data } = await listFiles({ query, throwOnError: true })
    return { kind: 'listed', folder: { ...data, entries: data.entries ?? [] } }
  } catch (err) {
    if (err instanceof ApiError && err.status === 503 && err.body.reason) return { kind: 'opening', reason: err.body }
    throw err
  }
}

/** Asks the gateway to get the home folder ready before the picker opens; a hint, so failures are ignored. */
export function warmHomeFolder(): void {
  warmFiles({ throwOnError: true }).catch(() => {})
}

/**
 * How long to wait before asking again, by how long the folder has been
 * opening or updating: half a second, then one, then every two.
 */
export function pollDelay(waitedMs: number): number {
  if (waitedMs < 500) return 500
  if (waitedMs < 1500) return 1000
  return 2000
}

/** The URL that shows or downloads one file of the user's home through the gateway. */
export function rawFileUrl(path: string): string {
  return `/api/files/raw?path=${encodeURIComponent(path)}`
}

/** Whether a home file is an HTML page, which the file viewer shows only in a sandboxed frame. */
export function isHtmlPage(path: string): boolean {
  return path.toLowerCase().endsWith('.html')
}

/** The file viewer's address for one file of the home; page shows an HTML file as a page. */
export function fileViewUrl(path: string, page = false): string {
  return `/files/view?path=${encodeURIComponent(path)}${page ? '&show=page' : ''}`
}

/**
 * Where a link that opens a home file goes: an HTML page to the file viewer,
 * which frames it in a sandbox (the gateway serves it only into a frame,
 * otherwise as a download); anything else to the file itself.
 */
export function openFileUrl(path: string): string {
  return isHtmlPage(path) ? fileViewUrl(path, true) : rawFileUrl(path)
}

/** A file size in words: bytes below 1 KB, else KB, MB and up with one decimal under 10. */
export function formatSize(n: number): string {
  if (n < 1024) return `${n} B`
  const units = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024
  let i = 0
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v < 10 ? v.toFixed(1).replace(/\.0$/, '') : Math.round(v)} ${units[i]}`
}
