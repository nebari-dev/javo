/** Links into Javo's GitHub repository at the build the server runs (GET /api/info). */

const REPO = 'https://github.com/nebari-dev/javo'
const RELEASE = /^v\d+\.\d+\.\d+$/

/** The running build: its release tag (or "dev") and full commit. */
export interface Build {
  version?: string
  commit?: string
}

const isRelease = (b?: Build): b is Build & { version: string } => RELEASE.test(b?.version ?? '')

/** What GitHub knows the build by: the release tag, else the commit, else main. */
function ref(b?: Build): string {
  if (isRelease(b)) return b.version
  return b?.commit || 'main'
}

/** The build's name and its page: a release's tag and release page, or dev+<short commit> and the commit. */
export function buildLink(b: Build): { label: string; href: string } {
  if (isRelease(b)) return { label: b.version, href: `${REPO}/releases/tag/${b.version}` }
  if (b.commit) return { label: `${b.version || 'dev'}+${b.commit.slice(0, 7)}`, href: `${REPO}/commit/${b.commit}` }
  return { label: b.version || 'dev', href: REPO }
}

/** A file of the repository (a path from its root) as the build has it. */
export function sourceLink(b: Build | undefined, path: string): string {
  return `${REPO}/blob/${ref(b)}/${path}`
}
