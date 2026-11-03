import { isFailedOutcome, outcomeSummary } from './format'

const cellClass = (o: string) => (o === 'Succeeded' ? 'ok' : o === 'Running' ? 'now' : isFailedOutcome(o) ? 'bad' : 'idle')

/** The outcomes of the latest runs, oldest first, as small cells with a text alternative. */
export function RunStrip({ recent }: { recent: string[] }) {
  if (recent.length === 0) return <span className="faint small">No runs yet</span>
  return (
    <span className="run-strip" role="img" aria-label={outcomeSummary(recent)}>
      {recent.map((o, i) => (
        <span key={i} className={`run-cell ${cellClass(o)}`} />
      ))}
    </span>
  )
}
