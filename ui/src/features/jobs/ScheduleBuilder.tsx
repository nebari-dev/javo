import { useId, useState } from 'react'
import { Select } from '../../components/Select'
import { TimePicker } from '../../components/TimePicker'
import { WeekdayPicker } from '../../components/WeekdayPicker'
import { cronText } from './format'
import { fromCron, MINUTE_STEPS, toCron, type Schedule, type ScheduleKind } from './schedule'

const KINDS: [ScheduleKind, string][] = [
  ['minute', 'Every minute'],
  ['minutes', 'Every few minutes'],
  ['hourly', 'Hourly'],
  ['daily', 'Daily'],
  ['weekly', 'Weekly'],
  ['monthly', 'Monthly'],
  ['custom', 'Custom (cron)'],
]

const pad = (n: number) => String(n).padStart(2, '0')

function ordinal(n: number): string {
  const tens = n % 100
  if (tens >= 11 && tens <= 13) return `${n}th`
  return `${n}${['th', 'st', 'nd', 'rd'][n % 10] ?? 'th'}`
}

/** The new schedule of another kind, keeping the time of day (and minute) already chosen. */
function switchTo(kind: ScheduleKind, from: Schedule): Schedule {
  const hour = 'hour' in from ? from.hour : 9
  const minute = 'minute' in from ? from.minute : 0
  switch (kind) {
    case 'minute':
      return { kind }
    case 'minutes':
      return { kind, every: 5 }
    case 'hourly':
      return { kind, minute }
    case 'daily':
      return { kind, hour, minute }
    case 'weekly':
      return { kind, days: [1], hour, minute }
    case 'monthly':
      return { kind, day: 1, hour, minute }
    case 'custom':
      return { kind, cron: toCron(from) }
  }
}

interface ScheduleBuilderProps {
  value: Schedule
  /** typed is true when the change came from typing in the cron field. */
  onChange: (s: Schedule, typed?: boolean) => void
  /** The server's word on a custom expression. */
  cronError?: string
  /** The cron field was left: time to check a custom expression. */
  onCronBlur: () => void
}

/**
 * When a job runs, chosen in words: a kind of schedule and the pickers it
 * needs. It writes the cron expression and shows it under its plain-English
 * reading in a field that can be edited too: a typed expression that is
 * one of the presets sets the pickers, anything else is "Custom (cron)".
 */
export function ScheduleBuilder({ value, onChange, cronError, onCronBlur }: ScheduleBuilderProps) {
  const ids = { kind: useId(), every: useId(), minute: useId(), day: useId(), cron: useId(), cronErr: useId(), cronHint: useId() }
  const cron = toCron(value)
  // What is typed in the cron field while it has focus, so reading it back into a preset never rewrites it under the cursor.
  const [draft, setDraft] = useState<string | null>(null)
  const text = cron ? cronText(cron) : ''
  const at = <S extends Extract<Schedule, { hour: number }>>(s: S) => (
    <TimePicker label="At" hour={s.hour} minute={s.minute} onChange={(hour, minute) => onChange({ ...s, hour, minute })} />
  )
  return (
    <div className="sched">
      <div className="field">
        <label htmlFor={ids.kind}>Repeats</label>
        <Select id={ids.kind} value={value.kind} onValueChange={(v) => onChange(switchTo(v as ScheduleKind, value))}>
          {KINDS.map(([k, l]) => (
            <option key={k} value={k}>
              {l}
            </option>
          ))}
        </Select>
      </div>

      {value.kind === 'minutes' && (
        <div className="field">
          <label htmlFor={ids.every}>Every</label>
          <Select id={ids.every} value={value.every} onValueChange={(v) => onChange({ ...value, every: Number(v) })}>
            {MINUTE_STEPS.map((n) => (
              <option key={n} value={n}>
                {n} minutes
              </option>
            ))}
          </Select>
        </div>
      )}
      {value.kind === 'hourly' && (
        <div className="field">
          <label htmlFor={ids.minute}>At minute</label>
          <Select id={ids.minute} value={value.minute} onValueChange={(v) => onChange({ ...value, minute: Number(v) })}>
            {Array.from({ length: 60 }, (_, m) => (
              <option key={m} value={m}>
                :{pad(m)}
              </option>
            ))}
          </Select>
        </div>
      )}
      {value.kind === 'daily' && at(value)}
      {value.kind === 'weekly' && (
        <>
          <WeekdayPicker
            label="On these days"
            value={value.days}
            onChange={(days) => onChange({ ...value, days })}
            error={value.days.length === 0 ? 'Pick at least one day' : undefined}
          />
          {at(value)}
        </>
      )}
      {value.kind === 'monthly' && (
        <div className="sched-pair">
          <div className="field">
            <label htmlFor={ids.day}>On day</label>
            <Select id={ids.day} value={value.day} onValueChange={(v) => onChange({ ...value, day: Number(v) })}>
              {Array.from({ length: 31 }, (_, i) => (
                <option key={i + 1} value={i + 1}>
                  {ordinal(i + 1)}
                </option>
              ))}
            </Select>
          </div>
          {at(value)}
        </div>
      )}
      {value.kind === 'monthly' && value.day > 28 && <p className="muted small sched-note">Months without a {ordinal(value.day)} are skipped.</p>}
      <div className="sched-sum">
        {text && text !== cron && !cronError && <p className="sched-desc">{text}</p>}
        <div className="sched-cron">
          <label htmlFor={ids.cron}>Cron</label>
          <input
            id={ids.cron}
            className={`input mono${cronError ? ' err' : ''}`}
            value={draft ?? cron}
            placeholder="0 6 * * 1"
            spellCheck={false}
            autoCapitalize="off"
            autoComplete="off"
            onFocus={() => setDraft(cron)}
            onChange={(e) => {
              setDraft(e.target.value)
              onChange(fromCron(e.target.value), true)
            }}
            onBlur={() => {
              setDraft(null)
              onCronBlur()
            }}
            aria-invalid={cronError ? true : undefined}
            aria-describedby={[value.kind === 'custom' ? ids.cronHint : '', cronError ? ids.cronErr : ''].filter(Boolean).join(' ') || undefined}
          />
        </div>
        {value.kind === 'custom' && (
          <span id={ids.cronHint} className="hint">
            Five fields: minute, hour, day of the month, month, day of the week.
          </span>
        )}
        {cronError && (
          <span id={ids.cronErr} className="errmsg">
            {cronError}
          </span>
        )}
      </div>
    </div>
  )
}
