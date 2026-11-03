import { Info } from 'lucide-react'
import { dstNote, type DstWarning } from './dstNote'
import { when, whenUtc } from './format'
import { PartIcon } from '../../components/Section'

interface PreviewData {
  next: string[]
  warnings: DstWarning[]
}

/**
 * The next runs in the schedule's zone and in UTC, under one quiet note about
 * the year's daylight saving changes with a way to sidestep them.
 */
export function SchedulePreview({ preview, timeZone, onUseUtc }: { preview: PreviewData; timeZone: string; onUseUtc: () => void }) {
  const note = timeZone === 'UTC' ? '' : dstNote(preview.warnings)
  return (
    <div className="preview">
      {note && (
        <p className="dst-note">
          <Info size={16} aria-hidden="true" className="dst-icon" />
          <span>
            {note}{' '}
            <button type="button" className="textlink" onClick={onUseUtc}>
              Use UTC
            </button>
          </span>
        </p>
      )}
      <div className="preview-runs">
        <div className="k">
          <PartIcon part="schedule" />
          Next runs
        </div>
        <ul className="preview-list" aria-label="Next runs">
          {preview.next.map((t) => (
            <li key={t}>
              <span>{when(t, timeZone)}</span>
              {timeZone !== 'UTC' && <span className="faint"> ({whenUtc(t)})</span>}
            </li>
          ))}
        </ul>
      </div>
    </div>
  )
}
