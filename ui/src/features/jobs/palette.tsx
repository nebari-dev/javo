import { Play, Plus } from 'lucide-react'
import { useNavigate } from 'react-router'
import type { JobView } from '../../api'
import { KindLogo } from '../../components/KindLogo'
import { PaletteAction, usePalette, type PaletteItem, type PaletteSection } from '../palette/Palette'
import { engineKind } from './format'
import { LastRunPill } from './JobsList'
import { useJobs, useRunJob } from './queries'

function RunNow({ job }: { job: JobView }) {
  const close = usePalette()
  const navigate = useNavigate()
  const run = useRunJob()
  return (
    <PaletteAction
      icon={Play}
      label="Run now"
      onSelect={() =>
        run.mutate(
          { name: job.name },
          {
            onSuccess: () => {
              close()
              void navigate(`/jobs/${encodeURIComponent(job.name)}`)
            },
          },
        )
      }
    />
  )
}

/** The user's jobs (Enter opens one; Run now is an action) and New job. */
export function useJobsPalette(): { section: PaletteSection; actions: PaletteItem[] } {
  const navigate = useNavigate()
  const jobs = useJobs().data ?? []
  return {
    section: {
      heading: 'Jobs',
      items: jobs.map((job) => ({
        id: `job:${job.name}`,
        label: job.displayName,
        keywords: [job.name, job.engine, job.source, job.command ?? ''],
        icon: <KindLogo kind={engineKind(job.engine)} />,
        meta: <LastRunPill job={job} />,
        run: () => void navigate(`/jobs/${encodeURIComponent(job.name)}`),
        actions: <RunNow job={job} />,
      })),
    },
    actions: [{ id: 'do:new-job', label: 'New job', keywords: ['create', 'schedule'], icon: <Plus size={16} />, run: () => void navigate('/jobs/new') }],
  }
}
