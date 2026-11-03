import type { UseMutationResult } from '@tanstack/react-query'
import { PartIcon } from '../../components/Section'
import { useState } from 'react'
import type { EnvVar } from '../../api'
import { fieldErrors } from '../../lib/http'
import { useSecrets } from '../../lib/queries'
import { Button } from '../../components/Button'
import { EnvEditor, envBody, envChanged, envLabel, rowsOf, type EnvRow } from './EnvEditor'

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
        {now.length === 0
          ? 'None'
          : now.map((e) => (
              <span key={e.name} className="mono block env-shown">
                {envLabel(e)}
              </span>
            ))}
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
