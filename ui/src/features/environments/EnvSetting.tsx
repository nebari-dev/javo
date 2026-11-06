import type { UseMutationResult } from '@tanstack/react-query'
import { EyeOff, LockKeyhole } from 'lucide-react'
import { PartIcon } from '../../components/Section'
import { useState } from 'react'
import type { EnvVar } from '../../api'
import { fieldErrors } from '../../lib/http'
import { useSecrets } from '../../lib/queries'
import { Button } from '../../components/Button'
import { EnvEditor, envBody, envChanged, rowsOf, type EnvRow } from './EnvEditor'

/** Where one variable's value comes from: the value itself, the secret it is read from, or that it is hidden. */
function EnvValue({ v }: { v: EnvVar }) {
  if (v.secret)
    return (
      <span className="env-tag" title={`From the secret ${v.secret}`}>
        <LockKeyhole size={13} aria-hidden="true" />
        <span className="sr-only">Secret </span>
        <span className="mono">{v.secret}</span>
      </span>
    )
  if (v.hidden)
    return (
      <span className="env-tag" title="Hidden: only its owner sees it">
        <EyeOff size={13} aria-hidden="true" />
        Hidden
      </span>
    )
  return (
    <span className="env-value mono" title={v.value ?? ''}>
      {v.value ?? ''}
    </span>
  )
}

/**
 * The environment variables row of a details list (`dl.kv`): what an app or a
 * job's runs get now, and behind Change the editor. `note` says when a change
 * applies.
 */
export function EnvSetting({
  now,
  note,
  hint,
  save,
}: {
  now: EnvVar[]
  note: string
  hint: string
  save: UseMutationResult<unknown, Error, EnvVar[]>
}) {
  const [rows, setRows] = useState<EnvRow[] | null>(null)
  const mine = useSecrets({ enabled: rows !== null }).data ?? []
  const errs = save.isError ? fieldErrors(save.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => f !== 'env' && !f.startsWith('env[')).map(([, m]) => m)]
  return (
    <>
      <dt className={now.length ? 'wide' : undefined}>
        <PartIcon part="variables" />
        Environment variables
      </dt>
      <dd className={now.length ? 'wide' : undefined}>
        {now.length === 0 ? (
          'None'
        ) : (
          // A row each: the name on the left, its value or secret on the right, cut with … when long and whole on hover.
          <ul className="env-rows">
            {now.map((e) => (
              <li key={e.name}>
                <span className="env-name mono" title={e.name}>
                  {e.name}
                </span>
                <EnvValue v={e} />
              </li>
            ))}
          </ul>
        )}
      </dd>
      <dd className="size-change">
        {rows === null ? (
          <Button size="sm" onClick={() => setRows(rowsOf(now))}>
            Change variables
          </Button>
        ) : (
          <div className="stack">
            <EnvEditor rows={rows} available={mine.map((s) => s.name)} errors={errs.fields} onChange={setRows} hint={hint} />
            <p className="tight muted small">{note}</p>
            {other.length > 0 && <span className="errmsg">{other.join(' ')}</span>}
            <div className="row">
              <Button
                size="sm"
                variant="primary"
                disabled={save.isPending || !envChanged(rows, now)}
                onClick={() => save.mutate(envBody(rows), { onSuccess: () => setRows(null) })}
              >
                Save variables
              </Button>
              <Button
                size="sm"
                onClick={() => {
                  save.reset()
                  setRows(null)
                }}
              >
                Cancel
              </Button>
            </div>
          </div>
        )}
      </dd>
    </>
  )
}
