import { fileViewUrl } from './files'

/** Where the person's home folder is mounted in their app and job pods. */
export const HOME_DIR = '/home/user'

/**
 * Where the Files page shows path, a path in the pod: a file in the file
 * view, a folder (kind says so, or the path ends in a slash) in the file
 * browser. Null for a path outside the home.
 */
export function homeFileLink(path: string, kind: 'file' | 'folder' = 'file'): string | null {
  if (path !== HOME_DIR && !path.startsWith(`${HOME_DIR}/`)) return null
  const parts = path.slice(HOME_DIR.length + 1).split('/')
  if (parts.some((p) => p === '..' || p === '.')) return null
  const folder = kind === 'folder' || parts[parts.length - 1] === ''
  const rel = parts.filter(Boolean).join('/')
  if (!rel) return '/files'
  return folder ? `/files?path=${encodeURIComponent(rel)}` : fileViewUrl(rel)
}
