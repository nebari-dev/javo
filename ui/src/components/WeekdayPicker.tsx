import { useId } from 'react'

// Monday first, as most calendars outside the US; values are cron's (0 is Sunday).
const DAYS: [number, string, string][] = [
  [1, 'Mon', 'Monday'],
  [2, 'Tue', 'Tuesday'],
  [3, 'Wed', 'Wednesday'],
  [4, 'Thu', 'Thursday'],
  [5, 'Fri', 'Friday'],
  [6, 'Sat', 'Saturday'],
  [0, 'Sun', 'Sunday'],
]

/** Days of the week as a row of toggle keys; any number can be on. */
export function WeekdayPicker({ label, value, onChange, error }: { label: string; value: number[]; onChange: (days: number[]) => void; error?: string }) {
  const id = useId()
  const errId = useId()
  return (
    <div className="field">
      <span className="field-label" id={id}>
        {label}
      </span>
      <div className="seg days" role="group" aria-labelledby={id} aria-describedby={error ? errId : undefined}>
        {DAYS.map(([d, short, long]) => {
          const on = value.includes(d)
          return (
            <button
              key={d}
              type="button"
              aria-label={long}
              aria-pressed={on}
              className={on ? 'on' : ''}
              onClick={() => onChange(on ? value.filter((x) => x !== d) : [...value, d])}
            >
              {short}
            </button>
          )
        })}
      </div>
      {error && (
        <span id={errId} className="errmsg">
          {error}
        </span>
      )}
    </div>
  )
}
