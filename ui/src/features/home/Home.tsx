import { CircleAlert, Hourglass } from 'lucide-react'
import { Link } from 'react-router'
import { useAppsView, ViewSwitch } from '../apps/AppViews'
import { AppsList, ShowFilter, useShowFilter } from '../apps/AppsFilter'
import { Banner } from '../../components/Banner'
import { Card } from '../../components/Card'
import { More } from '../../components/More'
import { ScrollBox } from '../../components/ScrollBox'
import { SampleGallery } from '../templates/SampleGallery'
import { PartTitle } from '../../components/Section'
import { useFeatureAttention, features, type AttentionEntry } from '..'
import { problemText } from '../../lib/http'
import { useStorage, useTemplates, useWorkspaces } from '../../lib/queries'
import { StorageNotices } from '../apps/StorageNotices'
import { count } from '../../lib/plural'
import { problemStep } from '../apps/status'

const SHOWN_ATTENTION = 2

function AttentionItem({ item }: { item: AttentionEntry }) {
  const Icon = item.failed ? CircleAlert : Hourglass
  return (
    <Link to={item.to} className="attn-item">
      <span className={`attn-ico ${item.failed ? 's-failed' : 's-pending'}`}>
        <Icon size={17} aria-hidden="true" />
      </span>
      <span className="grow">
        <span className="attn-name">{item.name}</span>
        <span className="muted small block">{item.note}</span>
      </span>
      <span className="btn sm">{item.action}</span>
    </Link>
  )
}

export function Home() {
  const apps = useWorkspaces()
  const storage = useStorage()
  const templates = useTemplates()
  const { show, setShow, shared, sharedCount } = useShowFilter()
  const list = apps.data ?? []
  const total = (show === 'shared' ? 0 : list.length) + (show === 'mine' ? 0 : sharedCount)
  const featureAttention = useFeatureAttention()
  const attention: AttentionEntry[] = [
    ...list
      .filter((w) => problemStep(w))
      .map((w) => ({
        key: `app:${w.name}`,
        to: `/apps/${encodeURIComponent(w.name)}`,
        name: w.displayName,
        note: problemStep(w)?.title,
        failed: w.phase === 'Failed',
        action: w.phase === 'Failed' ? 'See fix' : 'Details',
      })),
    ...featureAttention,
  ]
  const running = list.filter((w) => w.phase === 'Running').length
  const [view, setView] = useAppsView()

  return (
    <>
      {/* The filter sits in the page head, above everything that comes and goes, so it never moves. */}
      <div className="section-head page-head">
        <h1>Home</h1>
        <div className="right row">
          <ShowFilter show={show} onChange={setShow} />
          <ViewSwitch view={view} onChange={setView} />
        </div>
      </div>
      <p className="sub">{apps.data ? `${running} ${running === 1 ? 'app' : 'apps'} running` : ' '}</p>
      {apps.isError && (
        <div className="section">
          <Banner tone="err" title={problemText(apps.error)} />
        </div>
      )}

      {!!storage.data?.notices?.length && (
        <div className="section">
          <StorageNotices notices={storage.data.notices} />
        </div>
      )}

      {attention.length > 0 && (
        <section className="section" aria-labelledby="attn-h">
          <div className="section-head">
            <PartTitle part="attention" id="attn-h">
              Needs your attention
            </PartTitle>
            <span className="sub">
              {attention.length} {attention.length === 1 ? 'item' : 'items'}
            </span>
          </div>
          <Card className="attn">
            {attention.slice(0, SHOWN_ATTENTION).map((a) => (
              <AttentionItem key={a.key} item={a} />
            ))}
            {attention.length > SHOWN_ATTENTION && (
              <div className="attn-more">
                <More closed={`Show ${count(attention.length - SHOWN_ATTENTION, 'more item')}`} open={`Hide ${count(attention.length - SHOWN_ATTENTION, 'item')}`}>
                  {attention.slice(SHOWN_ATTENTION).map((a) => (
                    <AttentionItem key={a.key} item={a} />
                  ))}
                </More>
              </div>
            )}
          </Card>
        </section>
      )}

      {apps.data && (
        <section className="section" aria-labelledby="apps-h">
          <div className="section-head">
            <PartTitle part="apps" id="apps-h">
              Apps
            </PartTitle>
            <span className="sub">{total}</span>
          </div>
          <ScrollBox label="All apps">
            <AppsList mine={list} shared={shared} show={show} view={view} />
          </ScrollBox>
        </section>
      )}

      {features.flatMap((f) => f.homeSections ?? []).map((Section, i) => (
        <Section key={i} />
      ))}

      <SampleGallery templates={templates.data ?? []} />

    </>
  )
}
