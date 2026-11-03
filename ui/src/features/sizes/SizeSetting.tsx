import type { UseMutationResult } from '@tanstack/react-query'
import { PartIcon } from '../../components/Section'
import { useEffect, useId, useRef } from 'react'
import type { Amount } from '../../api'
import { Button } from '../../components/Button'
import { SizePicker } from './SizePicker'
import { fieldErrors, problemText } from '../../lib/http'
import { useCustomAllowance, useSizes } from '../../lib/queries'
import { choiceReady, customLabel, sameChoice, sizeLabel, type SizeChoice } from '../../lib/sizes'

interface SizeSettingProps {
  /** What it runs at now. */
  now: SizeChoice
  /** What saving does, in a few words under the sizes. */
  note: string
  /** Saves a new size or amount. */
  save: UseMutationResult<unknown, Error, SizeChoice>
  /** Whether Change size is offered. */
  canChange: boolean
  /** The size being chosen, null while closed; the page holds it so a failure card's Change size can open it too. */
  picked: SizeChoice | null
  setPicked: (s: SizeChoice | null) => void
  /** Counts the times something else on the page opened it, to bring it into view. */
  reveal: number
}

/**
 * The size an app or a job runs at, as rows of a key-value list, and behind
 * Change size the sizes to move it to.
 */
export function SizeSetting({ now, note, save, canChange, picked, setPicked, reveal }: SizeSettingProps) {
  const sizes = useSizes().data ?? []
  const allowance = useCustomAllowance()
  const ids = { head: useId(), err: useId() }
  const choices = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (reveal > 0) choices.current?.scrollIntoView?.({ block: 'nearest' })
  }, [reveal])
  if ('size' in now && !now.size) return null
  const current = 'size' in now ? sizes.find((s) => s.name === now.size) : undefined
  const fields = save.isError ? fieldErrors(save.error).fields : {}
  const err = save.isError && !fields.resources ? (fields.size ?? problemText(save.error)) : undefined
  return (
    <>
      <dt id={ids.head}>
        <PartIcon part="size" />
        Size
      </dt>
      <dd>{'resources' in now ? customLabel(now.resources) : current ? sizeLabel(current) : now.size}</dd>
      {sizes.length > 0 && canChange && (
        <dd className="size-change">
          {picked === null ? (
            <Button size="sm" onClick={() => setPicked(now)}>
              Change size
            </Button>
          ) : (
            <div className="stack" ref={choices}>
              <SizePicker
                sizes={sizes}
                custom={allowance}
                value={picked}
                onChange={setPicked}
                labelledBy={ids.head}
                describedBy={err ? ids.err : undefined}
                customError={fields.resources}
              />
              <p className="tight muted small">{note}</p>
              {err && (
                <span id={ids.err} className="errmsg">
                  {err}
                </span>
              )}
              <div className="row">
                <Button
                  size="sm"
                  variant="primary"
                  disabled={save.isPending || sameChoice(picked, now) || !choiceReady(allowance, picked)}
                  onClick={() => save.mutate(picked, { onSuccess: () => setPicked(null) })}
                >
                  Save size
                </Button>
                <Button
                  size="sm"
                  onClick={() => {
                    save.reset()
                    setPicked(null)
                  }}
                >
                  Cancel
                </Button>
              </div>
            </div>
          )}
        </dd>
      )}
    </>
  )
}

/** What a view with a size and an optional custom amount runs at, as a size picker value. */
export function choiceOf(v: { size: string; resources?: Amount | null }): SizeChoice {
  return v.resources ? { resources: v.resources } : { size: v.size }
}
