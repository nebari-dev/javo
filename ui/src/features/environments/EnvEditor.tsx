import { Plus, X } from 'lucide-react'
import { useId } from 'react'
import { Link } from 'react-router'
import type { EnvVar } from '../../api'
import { Button } from '../../components/Button'
import { IconButton } from '../../components/IconButton'
import { SearchSelect } from '../../components/SearchSelect'
import { Select } from '../../components/Select'

/**
 * One environment variable as the form holds it: a typed value, or a value
 * from one of the user's secrets. For a secret the name follows the secret's
 * until the user types one.
 */
export interface EnvRow {
  name: string
  nameTyped: boolean
  kind: 'value' | 'secret'
  value: string
  secret: string
}

/** What the editor says about an app's variables: who gets them and who could see them. */
export const APP_ENV_HINT =
  "Each is an environment variable of the app. A typed value is shown again; a secret's value never is. Anyone who can use the app could make it show them if the app prints them; sharing the app does not share the secrets."

/** What the editor says about a job's variables. */
export const JOB_ENV_HINT =
  "Each run gets each as an environment variable. A typed value is shown again; a secret's value never is. Anything a run prints, a value included, is kept in its log."

const emptyRow: EnvRow = { name: '', nameTyped: false, kind: 'value', value: '', secret: '' }

/** The rows for what an app or job has now. */
export const rowsOf = (list: EnvVar[] | null | undefined): EnvRow[] =>
  (list ?? []).map((e) =>
    e.secret ? { ...emptyRow, name: e.name, nameTyped: true, kind: 'secret', secret: e.secret } : { ...emptyRow, name: e.name, nameTyped: true, value: e.value ?? '' },
  )

/** The variable a row becomes: the typed name, or for a secret the secret's own. */
const nameOf = (r: EnvRow) => (r.nameTyped || r.kind === 'value' ? r.name.trim() : r.secret)

/** A row the request carries: one with a name, or with a secret picked. */
const filled = (r: EnvRow) => (r.kind === 'secret' ? !!r.secret : !!nameOf(r))

/** The request body: one entry per filled row. */
export const envBody = (rows: EnvRow[]): EnvVar[] =>
  rows.filter(filled).map((r) => (r.kind === 'secret' ? { name: nameOf(r) || r.secret, secret: r.secret } : { name: nameOf(r), value: r.value }))

const canonical = (list: EnvVar[]) =>
  JSON.stringify(
    list.map((e) => (e.secret ? { name: e.name, secret: e.secret } : { name: e.name, value: e.value ?? '' })).sort((a, b) => a.name.localeCompare(b.name)),
  )

/** True when the rows ask for something other than what there is now. */
export const envChanged = (rows: EnvRow[], now: EnvVar[] | null | undefined) => canonical(envBody(rows)) !== canonical(now ?? [])

/** "LOG_LEVEL=debug", or "OPENAI_API_KEY from OPENAI_KEY" for a secret: what a details list shows. */
export const envLabel = (e: EnvVar) =>
  e.secret ? `${e.name} from ${e.secret}` : e.hidden ? `${e.name} (value hidden: only its owner sees it)` : `${e.name}=${e.value ?? ''}`

function Row({
  row,
  index,
  available,
  errors,
  onChange,
  onRemove,
}: {
  row: EnvRow
  index: number | null
  available: string[]
  errors: Record<string, string>
  onChange: (r: EnvRow) => void
  onRemove: () => void
}) {
  const ids = { name: useId(), kind: useId(), value: useId(), secret: useId(), err: useId() }
  const err = (f: string) => (index === null ? undefined : errors[`env[${index}].${f}`])
  const nameErr = err('name')
  const valueErr = err('value') ?? err('secret')
  const messages = [nameErr, valueErr].filter(Boolean)
  const label = nameOf(row) || 'this variable'
  return (
    <li className="env-row">
      <div className="field env-name">
        <label htmlFor={ids.name}>Name</label>
        <input
          id={ids.name}
          className={`input mono${nameErr ? ' err' : ''}`}
          value={nameOf(row)}
          placeholder="LOG_LEVEL"
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => onChange({ ...row, name: e.target.value, nameTyped: true })}
          aria-invalid={nameErr ? true : undefined}
          aria-describedby={nameErr ? ids.err : undefined}
        />
      </div>
      <div className="field env-kind">
        <label htmlFor={ids.kind}>From</label>
        <Select id={ids.kind} value={row.kind} onValueChange={(v) => onChange({ ...row, kind: v as EnvRow['kind'] })}>
          <option value="value">Value</option>
          <option value="secret">Secret</option>
        </Select>
      </div>
      {row.kind === 'value' ? (
        <div className="field env-value">
          <label htmlFor={ids.value}>Value</label>
          <input
            id={ids.value}
            className={`input mono${valueErr ? ' err' : ''}`}
            value={row.value}
            placeholder="debug"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => onChange({ ...row, value: e.target.value })}
            aria-invalid={valueErr ? true : undefined}
            aria-describedby={valueErr ? ids.err : undefined}
          />
        </div>
      ) : available.length === 0 && !row.secret ? (
        <div className="field env-value">
          <span className="field-label">Secret</span>
          <span className="hint env-none">
            No secrets yet. <Link to="/settings/secrets?secret=new">Add one in Settings</Link>.
          </span>
        </div>
      ) : (
        <div className="env-value">
          <SearchSelect
            id={ids.secret}
            label="Secret"
            options={available}
            value={row.secret}
            onChange={(secret) => onChange({ ...row, secret })}
            invalid={!!valueErr}
            describedBy={valueErr ? ids.err : undefined}
          />
        </div>
      )}
      <IconButton className="env-remove" icon={X} label={`Remove ${label}`} onClick={onRemove} />
      {messages.length > 0 && (
        <span id={ids.err} className="errmsg env-err">
          {messages.join(' ')}
        </span>
      )}
    </li>
  )
}

/**
 * The environment variables of an app or a job's runs: each a
 * typed value, shown again, or a value from one of the user's secrets, of
 * which only the name passes through here.
 */
export function EnvEditor({
  rows,
  available,
  errors = {},
  onChange,
  hint,
}: {
  rows: EnvRow[]
  /** The names of the user's secrets. */
  available: string[]
  /** Field errors by request field (env, env[i].name, env[i].value, env[i].secret). */
  errors?: Record<string, string>
  onChange: (rows: EnvRow[]) => void
  /** Who gets the values and who could see them. */
  hint: string
}) {
  // A server error names a row by its place in the body, which leaves out empty rows.
  let n = 0
  const bodyIndex = rows.map((r) => (filled(r) ? n++ : null))
  return (
    <div className="stack env">
      {rows.length > 0 && (
        <ul className="env-list">
          {rows.map((r, i) => (
            <Row
              key={i}
              row={r}
              index={bodyIndex[i]}
              available={available}
              errors={errors}
              onChange={(next) => onChange(rows.map((x, j) => (j === i ? next : x)))}
              onRemove={() => onChange(rows.filter((_, j) => j !== i))}
            />
          ))}
        </ul>
      )}
      <div>
        <Button size="sm" icon={Plus} onClick={() => onChange([...rows, { ...emptyRow }])}>
          Add a variable
        </Button>
      </div>
      {errors.env && <span className="errmsg">{errors.env}</span>}
      <span className="hint">{hint}</span>
    </div>
  )
}
