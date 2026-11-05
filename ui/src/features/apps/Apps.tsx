import { AppsList, ShowFilter, useShowFilter } from './AppsFilter'
import { useAppsView, ViewSwitch } from './AppViews'
import { Banner } from '../../components/Banner'
import { StorageNotices } from './StorageNotices'
import { problemText } from '../../lib/http'
import { useStorage, useWorkspaces } from '../../lib/queries'

export function Apps() {
  const apps = useWorkspaces()
  const storage = useStorage()
  const [view, setView] = useAppsView()
  const { show, setShow, shared } = useShowFilter()
  return (
    <>
      <div className="section-head page-head">
        <h1>Apps</h1>
        <div className="right row">
          <ShowFilter show={show} onChange={setShow} />
          <ViewSwitch view={view} onChange={setView} />
        </div>
      </div>
      <p className="sub">Everything you run, and apps others shared with you. Stopped apps keep their settings and files.</p>
      {!!storage.data?.notices?.length && (
        <div className="section">
          <StorageNotices notices={storage.data.notices} />
        </div>
      )}
      <div className="section">
        {apps.isError && show !== 'shared' ? (
          <Banner tone="err" title={problemText(apps.error)} />
        ) : (
          (apps.data || show === 'shared') && <AppsList mine={apps.data ?? []} shared={shared} show={show} view={view} />
        )}
      </div>
    </>
  )
}
