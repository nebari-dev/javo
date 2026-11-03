import { useId } from 'react'
import type { IdleStopPolicy } from '../../api'
import { Select } from '../../components/Select'
import { Switch } from '../../components/Switch'

// The times offered besides the admin's own bounds and default.
const PRESETS = [15, 30, 60, 120, 240, 480, 1440, 4320, 10080]
// What turning the stop on picks when the admin's default is Off.
const ON_DEFAULT = 60

/** A timeout in words: "1 minute", "90 minutes", "2 hours", "7 days" (as the API's reasons word it). */
function minutesLabel(m: number): string {
  const [n, unit] = m >= 1440 && m % 1440 === 0 ? [m / 1440, 'day'] : m >= 120 && m % 60 === 0 ? [m / 60, 'hour'] : [m, 'minute']
  return `${n} ${unit}${n === 1 ? '' : 's'}`
}

/** An app's idle stop in words, beside "Stop when idle": "60 minutes" or "Off". */
export function idleStopLabel(minutes: number): string {
  return minutes > 0 ? minutesLabel(minutes) : 'Off'
}

/** The times to offer: the presets inside the allowed range, the bounds, the default and the current value. */
function choices(policy: IdleStopPolicy, value: number): number[] {
  const inRange = (m: number) => m >= policy.minMinutes && m <= policy.maxMinutes
  const all = new Set([...PRESETS.filter(inRange), policy.minMinutes, policy.maxMinutes, policy.defaultMinutes, value].filter((m) => m > 0))
  return [...all].sort((a, b) => a - b)
}

/** What turning the stop on picks: the admin's default, or an hour kept inside the range. */
function onValue(policy: IdleStopPolicy): number {
  if (policy.defaultMinutes > 0) return policy.defaultMinutes
  return Math.min(Math.max(ON_DEFAULT, policy.minMinutes), policy.maxMinutes)
}

/** "Stop when idle": a switch (when the caller may turn it off) and how long an app may sit unused. */
export function IdleStopChoice({
  policy,
  value,
  onChange,
  error,
}: {
  policy: IdleStopPolicy
  value: number
  onChange: (minutes: number) => void
  error?: string
}) {
  const selectId = useId()
  const errId = useId()
  const on = value > 0 || !policy.canTurnOff
  return (
    <div className="field fit tight">
      {policy.canTurnOff && <Switch label="Stop when idle" checked={on} onChange={(c) => onChange(c ? onValue(policy) : 0)} />}
      {on && (
        <>
          <label htmlFor={selectId}>Stop after</label>
          <Select
            id={selectId}
            value={String(value > 0 ? value : onValue(policy))}
            onValueChange={(v) => onChange(Number(v))}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? errId : undefined}
          >
            {choices(policy, value).map((m) => (
              <option key={m} value={m}>
                {minutesLabel(m)}
              </option>
            ))}
          </Select>
        </>
      )}
      <span className="hint">
        The app stops after this long with no visits and no open tabs. Its files and settings are kept, and it starts again from its card.
      </span>
      {error && (
        <span id={errId} className="errmsg">
          {error}
        </span>
      )}
    </div>
  )
}
