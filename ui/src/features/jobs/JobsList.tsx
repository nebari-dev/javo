import { Plus } from 'lucide-react'
import { Link, useNavigate } from 'react-router'
import type { JobView } from '../../api'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { EmptyState } from '../../components/EmptyState'
import { KindLogo } from '../../components/KindLogo'
import { Pill } from '../../components/Pill'
import { problemText } from '../../lib/http'
import { cronText, engineKind, outcomeLabel, outcomeStatus, when } from './format'
import { JobSamples } from './JobSamples'
import { useJobs } from './queries'
import { RunStrip } from './RunStrip'

export function LastRunPill({ job }: { job: Pick<JobView, 'lastRun'> }) {
  const r = job.lastRun
  return r ? <Pill status={outcomeStatus(r.outcome)} label={outcomeLabel(r.outcome)} /> : <span className="faint small">Never run</span>
}

/** When a job runs next, or why it does not. */
export function NextRun({ job }: { job: Pick<JobView, 'schedule' | 'nextRuns'> }) {
  const s = job.schedule
  if (!s) return <span className="faint">On demand</span>
  if (s.paused) return <span className="faint">Paused</span>
  const next = job.nextRuns?.[0]
  return <>{next ? when(next, s.timeZone) : ''}</>
}

function JobRow({ job }: { job: JobView }) {
  const s = job.schedule
  return (
    <tr>
      <th scope="row">
        <div className="atable-app">
          <KindLogo kind={engineKind(job.engine)} />
          <div className="atable-name">
            <Link to={`/jobs/${encodeURIComponent(job.name)}`}>{job.displayName}</Link>
            <span className="atable-why warn mono">{job.command || job.source}</span>
          </div>
        </div>
      </th>
      <td className="col-type">
        {s ? (
          <>
            {cronText(s.cron)}
            <span className="block faint small">{s.timeZone}</span>
          </>
        ) : (
          <span className="faint">Run on demand</span>
        )}
      </td>
      <td>
        <RunStrip recent={job.recent ?? []} />
      </td>
      <td>
        <LastRunPill job={job} />
      </td>
      <td className="col-file">
        <NextRun job={job} />
      </td>
    </tr>
  )
}

export function JobsList() {
  const jobs = useJobs()
  const navigate = useNavigate()
  const list = jobs.data ?? []
  return (
    <>
      <div className="section-head page-head">
        <h1 className="flush">Jobs</h1>
        <div className="right">
          <Button variant="primary" icon={Plus} onClick={() => navigate('/jobs/new')}>
            New job
          </Button>
        </div>
      </div>
      <p className="sub">Notebooks and scripts that run in the background.</p>
      {jobs.isError && (
        <div className="section">
          <Banner tone="err" title={problemText(jobs.error)} />
        </div>
      )}
      {jobs.data && (
        <section className="section">
          {list.length === 0 ? (
            <Card className="pad">
              <EmptyState
                title="No jobs yet"
                body="Run a script or notebook in the background, once or on a schedule."
              />
            </Card>
          ) : (
            <div className="card atable-wrap">
              <table className="atable">
                <caption className="sr-only">Your jobs</caption>
                <thead>
                  <tr>
                    <th scope="col">Job</th>
                    <th scope="col" className="col-type">
                      Schedule
                    </th>
                    <th scope="col">Last 12 runs</th>
                    <th scope="col">Last run</th>
                    <th scope="col" className="col-file">
                      Next
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {list.map((j) => (
                    <JobRow key={j.name} job={j} />
                  ))}
                </tbody>
              </table>
            </div>
          )}
        </section>
      )}
      <JobSamples />
    </>
  )
}
