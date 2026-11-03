import type { Notice } from '../../api'
import { Banner, type BannerTone } from '../../components/Banner'
import { More } from '../../components/More'

// The app page lists this one next to the app's details instead.
const APP_PAGE_ONLY = 'ScratchPaths'

const RANK: Record<BannerTone, number> = { err: 0, warn: 1, info: 2, ok: 3 }

function toneOf(n: Notice): BannerTone {
  return n.tone === 'err' || n.tone === 'warn' ? n.tone : 'info'
}

/** One notice as a banner: its title, text and action, all as the API sent them. */
export function NoticeBanner({ n }: { n: Notice }) {
  return (
    <Banner tone={toneOf(n)} title={n.title}>
      <p className="tight">{n.text}</p>
      {n.action && <p className="tight">{n.action}</p>}
    </Banner>
  )
}

/**
 * The storage notices the API sent, most urgent first. Only `limit` banners
 * show; the rest sit behind a Show N more control. All words come from the API.
 */
export function StorageNotices({ notices, limit = 1 }: { notices: Notice[] | null | undefined; limit?: number }) {
  const list = (notices ?? [])
    .filter((n) => n.code !== APP_PAGE_ONLY)
    .map((n, i) => ({ n, i }))
    .sort((a, b) => RANK[toneOf(a.n)] - RANK[toneOf(b.n)] || a.i - b.i)
    .map(({ n }) => n)
  if (list.length === 0) return null
  const rest = list.slice(limit)
  return (
    <div className="stack storage-notices">
      {list.slice(0, limit).map((n) => (
        <NoticeBanner key={n.code} n={n} />
      ))}
      {rest.length > 0 && (
        <More closed={`Show ${rest.length} more`} open="Show less">
          <div className="stack">
            {rest.map((n) => (
              <NoticeBanner key={n.code} n={n} />
            ))}
          </div>
        </More>
      )}
    </div>
  )
}
