import { useId } from 'react'
import { Select } from './Select'

const pad = (n: number) => String(n).padStart(2, '0')
const HOURS = Array.from({ length: 24 }, (_, i) => i)
const MINUTES = Array.from({ length: 60 }, (_, i) => i)

/** "13" or "01 PM": the hour on the locale's clock (the reader's when not given). */
function hourLabel(h: number, locale?: string): string {
  return new Intl.DateTimeFormat(locale, { hour: '2-digit', timeZone: 'UTC' }).format(Date.UTC(2000, 0, 1, h))
}

/** A time of day as two selects, hour (on the reader's clock) and minute, under one label. */
export function TimePicker({
  label,
  hour,
  minute,
  onChange,
  locale,
}: {
  label: string
  hour: number
  minute: number
  onChange: (hour: number, minute: number) => void
  locale?: string
}) {
  const id = useId()
  return (
    <div className="field">
      <span className="field-label" id={id}>
        {label}
      </span>
      <div className="timepick" role="group" aria-labelledby={id}>
        <Select aria-label="Hour" value={hour} onValueChange={(v) => onChange(Number(v), minute)}>
          {HOURS.map((h) => (
            <option key={h} value={h}>
              {hourLabel(h, locale)}
            </option>
          ))}
        </Select>
        <span className="timepick-sep" aria-hidden="true">
          :
        </span>
        <Select aria-label="Minute" value={minute} onValueChange={(v) => onChange(hour, Number(v))}>
          {MINUTES.map((m) => (
            <option key={m} value={m}>
              {pad(m)}
            </option>
          ))}
        </Select>
      </div>
    </div>
  )
}
