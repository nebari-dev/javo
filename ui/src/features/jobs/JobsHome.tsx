import { Link } from 'react-router'
import type { AttentionEntry } from '..'
import { KindLogo } from '../../components/KindLogo'
import { ScrollBox } from '../../components/ScrollBox'
import { PartTitle } from '../../components/Section'
import { cronText, engineKind } from './format'
import { LastRunPill } from './JobsList'
import { useJobs } from './queries'

/** Home's "Jobs": every job, scrolling in place, hidden until there is at least one. */
export function JobsHomeSection() {
  const jobs = useJobs().data ?? []
  if (jobs.length === 0) return null
  return (
    <section className="section" aria-labelledby="jobs-h">
      <div className="section-head">
        <PartTitle part="jobs" id="jobs-h">
          Jobs
        </PartTitle>
        <span className="sub">{jobs.length}</span>
      </div>
      <ScrollBox label="All jobs" className="card attn">
        {jobs.map((j) => (
          <Link key={j.name} to={`/jobs/${encodeURIComponent(j.name)}`} className="attn-item">
            <KindLogo kind={engineKind(j.engine)} />
            <span className="grow">
              <span className="attn-name">{j.displayName}</span>
              <span className="muted small block">{j.schedule ? cronText(j.schedule.cron) : 'Run on demand'}</span>
            </span>
            <LastRunPill job={j} />
          </Link>
        ))}
      </ScrollBox>
    </section>
  )
}

/** Jobs whose latest run failed, for Home's "Needs your attention". */
export function useJobsAttention(): AttentionEntry[] {
  return (useJobs().data ?? [])
    .filter((j) => j.problem)
    .map((j) => ({
      key: `job:${j.name}`,
      to: `/jobs/${encodeURIComponent(j.name)}`,
      name: j.displayName,
      note: j.problem?.title,
      failed: true,
      action: 'See fix',
    }))
}
