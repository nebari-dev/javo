import { KeyRound, Pencil, Plus, Trash2 } from 'lucide-react'
import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { useSearchParams } from 'react-router'
import type { SecretView } from '../../api'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { Confirm } from '../../components/Confirm'
import { EmptyState } from '../../components/EmptyState'
import { PartTitle } from '../../components/Section'
import { fieldErrors, problemText } from '../../lib/http'
import { AgentSettings } from './AgentSettings'
import { useCreateSecret, useDeleteSecret, useSecrets, useUpdateSecret } from '../../lib/queries'

const FORM_FIELDS = new Set(['name', 'value', 'note'])
const NAME_RE = /^[A-Za-z_][A-Za-z0-9_]*$/
const NEVER_AGAIN = 'The value is never shown again.'

/** "2 Nov 2026". */
function day(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric' }).format(d)
}

function FieldError({ id, msg }: { id: string; msg?: string }) {
  return msg ? (
    <span id={id} className="errmsg">
      {msg}
    </span>
  ) : null
}

/** A write-only value box: it starts empty and is cleared once the value is sent. */
function ValueField({ id, errId, value, onChange, error, label }: { id: string; errId: string; value: string; onChange: (v: string) => void; error?: string; label: string }) {
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <textarea
        id={id}
        className={`input mono sec-value${error ? ' err' : ''}`}
        value={value}
        rows={3}
        autoComplete="off"
        autoCorrect="off"
        autoCapitalize="off"
        spellCheck={false}
        data-1p-ignore
        onChange={(e) => onChange(e.target.value)}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? errId : undefined}
      />
      <span className="hint">{`Stored encrypted. ${NEVER_AGAIN}`}</span>
      <FieldError id={errId} msg={error} />
    </div>
  )
}

function SecretForm({ onCreated, onCancel }: { onCreated: (name: string) => void; onCancel: () => void }) {
  const create = useCreateSecret()
  const ids = { name: useId(), nameErr: useId(), value: useId(), valueErr: useId(), note: useId(), noteErr: useId() }
  const [name, setName] = useState('')
  const [value, setValue] = useState('')
  const [note, setNote] = useState('')
  const errs = create.isError ? fieldErrors(create.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => !FORM_FIELDS.has(f)).map(([, m]) => m)]
  const nameOK = NAME_RE.test(name.trim())

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!nameOK || !value) return
    const n = name.trim()
    create.mutate(
      { name: n, value, ...(note.trim() ? { note: note.trim() } : {}) },
      {
        onSuccess: () => {
          setValue('')
          onCreated(n)
        },
      },
    )
  }

  return (
    <Card className="pad tok-form">
      <form onSubmit={submit} noValidate>
        <PartTitle part="secrets" as="h3" className="card-h">
          New secret
        </PartTitle>
        {other.length > 0 && (
          <div className="field">
            <Banner tone="err" title={other.join(' ')} />
          </div>
        )}
        <div className="field">
          <label htmlFor={ids.name}>Name</label>
          <input
            id={ids.name}
            className={`input mono${errs.fields.name ? ' err' : ''}`}
            value={name}
            placeholder="OPENAI_API_KEY"
            maxLength={100}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={errs.fields.name ? true : undefined}
            aria-describedby={errs.fields.name ? ids.nameErr : undefined}
          />
          <span className="hint">Letters, digits and underscores. Apps and jobs get it under this name unless you pick another.</span>
          <FieldError id={ids.nameErr} msg={errs.fields.name} />
        </div>
        <ValueField id={ids.value} errId={ids.valueErr} label="Value" value={value} onChange={setValue} error={errs.fields.value} />
        <div className="field">
          <label htmlFor={ids.note}>Note</label>
          <input
            id={ids.note}
            className={`input${errs.fields.note ? ' err' : ''}`}
            value={note}
            placeholder="Optional: what it is for"
            maxLength={200}
            onChange={(e) => setNote(e.target.value)}
            aria-invalid={errs.fields.note ? true : undefined}
            aria-describedby={errs.fields.note ? ids.noteErr : undefined}
          />
          <FieldError id={ids.noteErr} msg={errs.fields.note} />
        </div>
        <div className="row tok-form-act">
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!nameOK || !value || create.isPending}>
            Save secret
          </Button>
        </div>
      </form>
    </Card>
  )
}

/** Replace the value (never shown, never prefilled) or change the note of one secret. */
function EditSecret({ s, what, onDone }: { s: SecretView; what: 'value' | 'note'; onDone: (saved: boolean) => void }) {
  const update = useUpdateSecret(s.name)
  const ids = { value: useId(), valueErr: useId(), note: useId(), noteErr: useId() }
  const [value, setValue] = useState('')
  const [note, setNote] = useState(s.note)
  const errs = update.isError ? fieldErrors(update.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => !FORM_FIELDS.has(f)).map(([, m]) => m)]
  const usedByApps = (s.apps ?? []).length > 0
  const usedByJobs = (s.jobs ?? []).length > 0
  const submit = (e: FormEvent) => {
    e.preventDefault()
    update.mutate(what === 'value' ? { value } : { note: note.trim() }, {
      onSuccess: () => {
        setValue('')
        onDone(true)
      },
    })
  }
  return (
    <form className="sec-edit stack" onSubmit={submit} noValidate>
      {other.length > 0 && <Banner tone="err" title={other.join(' ')} />}
      {what === 'value' ? (
        <>
          <ValueField id={ids.value} errId={ids.valueErr} label={`New value for ${s.name}`} value={value} onChange={setValue} error={errs.fields.value} />
          {usedByApps && <p className="tight muted small">Apps that use it restart to get the new value.</p>}
          {usedByJobs && <p className="tight muted small">Jobs that use it get the new value from their next run.</p>}
        </>
      ) : (
        <div className="field">
          <label htmlFor={ids.note}>Note</label>
          <input
            id={ids.note}
            className={`input${errs.fields.note ? ' err' : ''}`}
            value={note}
            maxLength={200}
            onChange={(e) => setNote(e.target.value)}
            aria-invalid={errs.fields.note ? true : undefined}
            aria-describedby={errs.fields.note ? ids.noteErr : undefined}
          />
          <FieldError id={ids.noteErr} msg={errs.fields.note} />
        </div>
      )}
      <div className="row tok-form-act">
        <Button size="sm" onClick={() => onDone(false)}>
          Cancel
        </Button>
        <Button size="sm" type="submit" variant="primary" disabled={update.isPending || (what === 'value' ? !value : note.trim() === s.note)}>
          {what === 'value' ? 'Save value' : 'Save note'}
        </Button>
      </div>
    </form>
  )
}

function SecretRow({ s, onSaved }: { s: SecretView; onSaved: (msg: string) => void }) {
  const del = useDeleteSecret(s.name)
  const [asking, setAsking] = useState(false)
  const [editing, setEditing] = useState<'value' | 'note' | null>(null)
  const btn = useRef<HTMLButtonElement>(null)
  const users = [...(s.apps ?? []).map((a) => a.displayName || a.name), ...(s.jobs ?? []).map((j) => `${j.displayName || j.name} (job)`)]
  return (
    <li className="tok-row sec-row">
      <div className="tok-main">
        <h3 className="tok-name mono sec-name">{s.name}</h3>
        {s.note && <span className="muted small">{s.note}</span>}
      </div>
      <dl className="tok-facts sec-facts">
        <div>
          <dt>Updated</dt>
          <dd>{day(s.updatedAt)}</dd>
        </div>
        <div>
          <dt>Used by</dt>
          <dd>{users.length ? users.join(', ') : 'Nothing'}</dd>
        </div>
      </dl>
      <div className="tok-act sec-act row">
        <Button size="sm" icon={KeyRound} aria-label={`Replace the value of ${s.name}`} disabled={!!editing} onClick={() => setEditing('value')}>
          <span className="tok-act-label">Replace value</span>
        </Button>
        <Button size="sm" variant="ghost" icon={Pencil} aria-label={`Edit the note of ${s.name}`} disabled={!!editing} onClick={() => setEditing('note')}>
          <span className="tok-act-label">Note</span>
        </Button>
        <Button ref={btn} size="sm" variant="ghost" icon={Trash2} aria-label={`Delete ${s.name}`} onClick={() => setAsking(true)}>
          <span className="tok-act-label">Delete</span>
        </Button>
      </div>
      {editing && (
        <div className="sec-row-full">
          <EditSecret
            s={s}
            what={editing}
            onDone={(saved) => {
              if (saved) onSaved(editing === 'value' ? `New value for ${s.name} saved. ${NEVER_AGAIN}` : `Note of ${s.name} saved.`)
              setEditing(null)
            }}
          />
        </div>
      )}
      {asking && (
        <Confirm
          title={`Delete ${s.name}?`}
          body={users.length ? 'Apps or jobs use this secret, so it cannot be deleted until you take it off them.' : 'It is gone for good. Nothing uses it.'}
          confirm="Delete"
          icon={Trash2}
          danger
          busy={del.isPending}
          error={del.isError ? del.error : undefined}
          onConfirm={() => del.mutate()}
          onCancel={() => {
            del.reset()
            setAsking(false)
            btn.current?.focus()
          }}
        />
      )}
    </li>
  )
}

/** Settings, Secrets: named values apps get as environment variables. Values are write-only. */
export function Secrets() {
  const secrets = useSecrets()
  const [creating, setCreating] = useState(false)
  const [saved, setSaved] = useState<string | null>(null)
  // ?secret=new (the palette's New secret) opens the form.
  const [params] = useSearchParams()
  const askNew = params.get('secret') === 'new'
  useEffect(() => {
    if (askNew) setCreating(true)
  }, [askNew])
  const list = secrets.data ?? []
  return (
    <section className="section" aria-label="Secrets">
      <div className="section-head tok-head">
        <p className="sub tok-intro">
          Apps and jobs you choose get these as environment variables. Anyone who can use an app could make it show them if the app prints its
          environment; sharing an app does not share the secrets themselves.
        </p>
        <div className="right row">
          <Button
            icon={Plus}
            disabled={creating}
            onClick={() => {
              setSaved(null)
              setCreating(true)
            }}
          >
            New secret
          </Button>
        </div>
      </div>
      {saved && (
        <div className="section-sm">
          <Banner tone="info" title={saved} />
        </div>
      )}
      {creating && (
        <div className="section-sm">
          <SecretForm
            onCreated={(n) => {
              setCreating(false)
              setSaved(`${n} saved. ${NEVER_AGAIN}`)
            }}
            onCancel={() => setCreating(false)}
          />
        </div>
      )}
      <div className="section-sm">
        {secrets.isError ? (
          <Banner tone="err" title={problemText(secrets.error)} />
        ) : secrets.isPending ? (
          <div className="muted">Loading</div>
        ) : list.length === 0 ? (
          <EmptyState title="No secrets yet" body="Add an API key or a password with New secret, then pick it for an app under Advanced, or for a job." />
        ) : (
          <Card>
            <ul className="tok-list" aria-label="Secrets">
              {list.map((s) => (
                <SecretRow key={s.name} s={s} onSaved={setSaved} />
              ))}
            </ul>
          </Card>
        )}
      </div>
      <AgentSettings />
    </section>
  )
}
