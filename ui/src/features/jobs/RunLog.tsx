import { LogView } from '../logs/LogView'
import { problemText } from '../../lib/http'
import { useRunLog } from './queries'

/**
 * One run's log in the standard log viewer: followed live while the run goes,
 * and read from the run folder once its pod is gone.
 */
export function RunLog({ job, run, running }: { job: string; run: string; running: boolean }) {
  const log = useRunLog(job, run, running)
  const text = log.data?.text.replace(/\n$/, '') ?? ''
  const lines = text ? text.split('\n').map((t, i) => ({ id: i, text: t })) : []
  return (
    <LogView
      title="Run log"
      label="Run log"
      lines={lines}
      status={log.data?.saved ? 'saved' : running ? 'live' : undefined}
      follow={running}
      // A run that goes but has not started its container shows the skeleton until it does.
      loading={log.isPending}
      error={log.error ? problemText(log.error) : undefined}
      empty={running ? { title: 'Nothing printed yet', body: 'Lines the run prints show up here as they happen.' } : { title: 'The run printed nothing', body: 'It finished without writing to its log.' }}
    />
  )
}
