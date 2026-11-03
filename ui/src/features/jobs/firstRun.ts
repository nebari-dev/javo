import { useNavigate } from 'react-router'
import { problemText } from '../../lib/http'
import type { JobPageState } from './JobDetail'
import { useRunJob } from './queries'

/**
 * Run once: start the first run of a job just made, then open it. When the
 * run cannot start the job page says why. New job and the sample job card
 * both use it.
 */
export function useStartFirstRun() {
  const navigate = useNavigate()
  const run = useRunJob()
  return (name: string) => {
    const open = (state?: JobPageState) => navigate(`/jobs/${encodeURIComponent(name)}`, { state })
    run.mutate({ name }, { onSuccess: () => open(), onError: (err) => open({ runError: problemText(err) }) })
  }
}
