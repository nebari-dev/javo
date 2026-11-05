import { useSearchParams } from 'react-router'
import type { Workspace } from '../../api'
import { problemText } from '../../lib/http'
import { useShared } from '../../lib/queries'
import { AppCard, NoApps } from './AppCard'
import { AppTable } from './AppTable'
import type { AppsView } from './AppViews'
import { Banner } from '../../components/Banner'
import { Card } from '../../components/Card'
import { Segmented } from '../../components/Segmented'
import { SharedCard } from './SharedCard'
import { listedKey, newestFirst } from './listed'

type Show = 'all' | 'mine' | 'shared'

/**
 * Which apps a list shows: all by default, or only the user's own, or only
 * those shared with them. Kept in the address (?show=mine|shared) so a reload
 * or a link keeps it. Always offered, so the control never appears, vanishes
 * or moves as the data changes.
 */
export function useShowFilter() {
  const [params, setParams] = useSearchParams()
  const shared = useShared()
  const q = params.get('show')
  const show: Show = q === 'mine' || q === 'shared' ? q : 'all'
  const setShow = (s: Show) =>
    setParams(
      (p) => {
        if (s === 'all') p.delete('show')
        else p.set('show', s)
        return p
      },
      { replace: true },
    )
  const sharedCount = shared.data?.items?.length ?? 0
  return { show, setShow, shared, sharedCount }
}

export function ShowFilter({ show, onChange }: { show: Show; onChange: (s: Show) => void }) {
  return (
    <Segmented
      label="Show"
      value={show}
      onChange={onChange}
      options={[
        { value: 'all', label: 'All' },
        { value: 'mine', label: 'Mine' },
        { value: 'shared', label: 'Shared with me' },
      ]}
    />
  )
}

/**
 * The user's apps and the apps shared with them, filtered by `show`, as one
 * list newest first: one grid of cards, or one table.
 */
export function AppsList(props: {
  mine: Workspace[]
  shared: ReturnType<typeof useShared>
  show: Show
  view: AppsView
}) {
  const { shared, show, view } = props
  const mine = show === 'shared' ? [] : props.mine
  const others = show === 'mine' ? [] : (shared.data?.items ?? [])
  const sharedErr = show !== 'mine' && shared.isError ? <Banner tone="err" title={problemText(shared.error)} /> : null
  if (mine.length + others.length === 0) {
    if (sharedErr) return sharedErr
    if (show === 'shared') return shared.data ? <Card className="empty">{shared.data.empty}</Card> : null
    return <NoApps />
  }
  const apps = newestFirst(mine, others)
  return (
    <>
      {sharedErr}
      {view === 'list' ? (
        <AppTable apps={apps} />
      ) : (
        <div className="grid">
          {apps.map((a) => (a.mine ? <AppCard key={listedKey(a)} ws={a.mine} /> : <SharedCard key={listedKey(a)} app={a.shared} />))}
        </div>
      )}
    </>
  )
}
