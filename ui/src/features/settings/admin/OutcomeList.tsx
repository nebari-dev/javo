import { AlertTriangle, Check, Minus, Plus } from 'lucide-react'
import type { TransferRow } from '../../../api'
import { More } from '../../../components/More'

/** Rows that cannot go ahead come first, then the ones left alone; the ones that go ahead wait behind More. */
const PROBLEM = new Set<TransferRow['outcome']>(['Invalid', 'TemplateMissing', 'OwnerUnknown'])
const AHEAD = new Set<TransferRow['outcome']>(['Create', 'Created'])

function Row({ r }: { r: TransferRow }) {
  const Icon = PROBLEM.has(r.outcome) ? AlertTriangle : AHEAD.has(r.outcome) ? (r.outcome === 'Created' ? Check : Plus) : Minus
  return (
    <li className={`outcome${PROBLEM.has(r.outcome) ? ' err' : ''}`}>
      <Icon size={16} aria-hidden="true" className="outcome-ico" />
      <div className="outcome-main">
        <span className="outcome-name">{r.displayName || r.name || 'Entry'}</span>
        <span className="small muted">{r.message}</span>
      </div>
    </li>
  )
}

/**
 * What a restore does, or would do, with each app. label names
 * the list; ahead names the rows that go ahead, for the More control.
 */
export function OutcomeList({ rows, label, ahead }: { rows: TransferRow[]; label: string; ahead: (n: number) => string }) {
  const first = [...rows.filter((r) => PROBLEM.has(r.outcome)), ...rows.filter((r) => !PROBLEM.has(r.outcome) && !AHEAD.has(r.outcome))]
  const going = rows.filter((r) => AHEAD.has(r.outcome))
  return (
    <div className="outcomes">
      {first.length > 0 && (
        <ul className="outcome-list" aria-label={label}>
          {first.map((r, i) => (
            <Row key={`${r.owner ?? ''}/${r.name}/${i}`} r={r} />
          ))}
        </ul>
      )}
      {going.length > 0 &&
        (first.length === 0 ? (
          <ul className="outcome-list" aria-label={label}>
            {going.map((r, i) => (
              <Row key={`${r.owner ?? ''}/${r.name}/${i}`} r={r} />
            ))}
          </ul>
        ) : (
          <More closed={ahead(going.length)} open={ahead(going.length)}>
            <ul className="outcome-list" aria-label={`${label}, going ahead`}>
              {going.map((r, i) => (
                <Row key={`${r.owner ?? ''}/${r.name}/${i}`} r={r} />
              ))}
            </ul>
          </More>
        ))}
    </div>
  )
}
