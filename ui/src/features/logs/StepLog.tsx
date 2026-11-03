import type { StepLogRecord } from '../../api'
import { useStepLog, type StreamedStep } from '../../lib/queries'
import type { LogLine } from './LogLines'
import { LogView } from './LogView'

// Never shown: the view appears only once there are records.
const NO_LINES = { title: 'No lines', body: '' }

/** A record as a log line: the container's lines as they are, the rest in the cluster's words. */
function asLine(r: StepLogRecord, i: number): LogLine {
  const base = { id: i, time: r.time }
  switch (r.kind) {
    case 'start':
      return { ...base, label: 'container', text: r.container }
    case 'exit': {
      const why = r.reason ? ` (${r.reason})` : ''
      const msg = r.text ? `: ${r.text}` : ''
      return { ...base, label: 'exit', text: `${r.container} exited with code ${r.exitCode ?? '?'}${why}${msg}`, warn: r.exitCode !== 0 }
    }
    case 'waiting':
      return { ...base, label: 'waiting', text: `${r.container}: ${[r.reason, r.text].filter(Boolean).join(': ')}`, warn: true }
    case 'restart':
      return { ...base, label: 'restart', text: `${r.container} restarted (restart ${r.restarts ?? 0})`, warn: true }
  }
  return { ...base, text: r.text ?? '' }
}

/**
 * The live output of the containers doing a startup step's work, one after
 * another, as the server writes it; followed again after it ends while the
 * step goes. Nothing is shown until there is output.
 */
export function StepLog({ name, step, following }: { name: string; step: StreamedStep; following: boolean }) {
  const records = useStepLog(name, step, following)
  if (!records || records.length === 0) return null
  return <LogView compact follow title="Output" label="Step output" status={following ? 'live' : undefined} empty={NO_LINES} lines={records.map(asLine)} />
}
