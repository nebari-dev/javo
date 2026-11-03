import { useId } from 'react'
import type { EnvironmentView } from '../../api'
import { useEnvironments } from '../../lib/queries'
import { Card } from '../../components/Card'
import { PartIcon, Section } from '../../components/Section'
import { Select } from '../../components/Select'

/**
 * The Environment card of a form that makes an app or a job: one of the
 * person's environments from the platform's environment manager, or none.
 * It renders nothing when the platform has no manager (or cannot say).
 */
export function EnvironmentCard({ value, onChange, error, what }: { value: string; onChange: (id: string) => void; error?: string; what: 'app' | 'job' }) {
  const list = useEnvironments().data
  const ids = { select: useId(), hint: useId(), err: useId() }
  if (!list?.available) return null
  const items = list.items ?? []
  return (
    <Section part="environment" title="Environment">
    <Card className="pad">
      <div className="field">
        <label htmlFor={ids.select} className="sr-only">
          Environment
        </label>
        <Select
          id={ids.select}
          value={value}
          onValueChange={(v) => onChange(v)}
          disabled={items.length === 0}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? ids.err : ids.hint}
          className={error ? 'err' : undefined}
        >
          <option value="">None: the template's own packages</option>
          {items.map((e) => (
            <option key={e.id} value={e.id}>
              {e.owner ? `${e.name} (${e.owner})` : e.name}
            </option>
          ))}
        </Select>
        <span id={ids.hint} className="muted small">
          {items.length === 0
            ? `${list.message ?? 'You have no environments yet, and none are shared with you.'} The ${what} runs with its template's own packages.`
            : list.message ??
              `The ${what} gets the environment's packages as they are now. Changes made to the environment later do not reach this ${what}.`}
        </span>
        {error && (
          <span id={ids.err} className="errmsg">
            {error}
          </span>
        )}
      </div>
    </Card>
    </Section>
  )
}

/** The environment row of an app's or a job's details; nothing without one. */
export function EnvironmentRow({ env }: { env?: EnvironmentView }) {
  if (!env) return null
  return (
    <>
      <dt>
        <PartIcon part="environment" />
        Environment
      </dt>
      <dd>{env.version ? `${env.name}, version ${env.version}` : env.name}</dd>
    </>
  )
}
