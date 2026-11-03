import { useId } from 'react'
import { Link } from 'react-router'
import { Select } from '../../components/Select'

/**
 * The access token of a private repository, picked from the user's secrets;
 * "" is none. With no secrets yet it says so and links to the
 * New secret form. Only the secret's name passes through here.
 */
export function TokenSecretField({
  label = 'Access token',
  value,
  onChange,
  available,
  hint,
  error,
}: {
  label?: string
  value: string
  onChange: (secret: string) => void
  /** The names of the user's secrets. */
  available: string[]
  hint: string
  error?: string
}) {
  const ids = { input: useId(), hint: useId(), err: useId() }
  if (available.length === 0 && !value) {
    return (
      <div className="field">
        <span className="field-label">{label}</span>
        <span className="hint">
          No secrets yet. A private repository needs its token stored as a secret first: <Link to="/settings/secrets?secret=new">add one in Settings</Link>.
        </span>
      </div>
    )
  }
  return (
    <div className="field">
      <label htmlFor={ids.input}>{label}</label>
      <Select
        id={ids.input}
        className={`mono${error ? ' err' : ''}`}
        value={value}
        onValueChange={(v) => onChange(v)}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${ids.hint} ${ids.err}` : ids.hint}
      >
        <option value="">None, the repository is public</option>
        {[...new Set([...available, ...(value ? [value] : [])])].map((n) => (
          <option key={n} value={n}>
            {n}
          </option>
        ))}
      </Select>
      <span id={ids.hint} className="hint">
        {hint}
      </span>
      {error && (
        <span id={ids.err} className="errmsg">
          {error}
        </span>
      )}
    </div>
  )
}
