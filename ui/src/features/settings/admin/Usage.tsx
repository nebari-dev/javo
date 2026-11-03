import { useState } from 'react'
import { Banner } from '../../../components/Banner'
import { Card } from '../../../components/Card'
import { EmptyState } from '../../../components/EmptyState'
import { PartIcon } from '../../../components/Section'
import { Segmented } from '../../../components/Segmented'
import { problemText } from '../../../lib/http'
import { useUsage } from './queries'

const BY = [
  { value: 'person', label: 'By person' },
  { value: 'group', label: 'By group' },
] as const
const DAYS = [
  { value: '7', label: '7 days' },
  { value: '30', label: '30 days' },
]

const hours = (h: number) => (Math.round(h * 10) / 10).toString()

/** Settings, Usage: app-hours per person or group in the last days. */
export function Usage() {
  const [by, setBy] = useState<'person' | 'group'>('person')
  const [days, setDays] = useState('7')
  const usage = useUsage(by, Number(days))
  return (
    <>
      <div className="row usage-controls">
        <Segmented label="Group usage" value={by} options={[...BY]} onChange={setBy} />
        <Segmented label="Window" value={days} options={DAYS} onChange={setDays} />
      </div>
      {usage.isError ? (
        <div className="section-sm">
          <Banner tone="err" title={problemText(usage.error)} />
        </div>
      ) : usage.isPending ? (
        <div className="section-sm muted">Loading</div>
      ) : (
        <>
          <div className="section-sm stat-row">
            <Card className="pad stat">
              <span className="stat-label">
                <PartIcon part="hours" />
                App-hours
              </span>
              <span className="stat-value">{hours(usage.data.totals.hours)}</span>
            </Card>
            <Card className="pad stat">
              <span className="stat-label">
                <PartIcon part="people" />
                People
              </span>
              <span className="stat-value">{usage.data.totals.people}</span>
            </Card>
            <Card className="pad stat">
              <span className="stat-label">
                <PartIcon part="running" />
                Running now
              </span>
              <span className="stat-value">{usage.data.totals.runningNow}</span>
            </Card>
          </div>
          <div className="section-sm">
            {(usage.data.items ?? []).length === 0 ? (
              <EmptyState title="No apps ran in this window" body="Hours show here once apps run." />
            ) : (
              <div className="card atable-wrap">
                <table className="atable">
                  <caption className="sr-only">Usage</caption>
                  <thead>
                    <tr>
                      <th scope="col">{by === 'person' ? 'Person' : 'Group'}</th>
                      <th scope="col">App-hours</th>
                      <th scope="col">Apps</th>
                      <th scope="col">Running now</th>
                    </tr>
                  </thead>
                  <tbody>
                    {(usage.data.items ?? []).map((r) => (
                      <tr key={r.key}>
                        <th scope="row">{r.key === '' ? 'In no group' : r.displayName}</th>
                        <td>{hours(r.hours)}</td>
                        <td>{r.apps}</td>
                        <td>{r.runningNow}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>
        </>
      )}
    </>
  )
}
