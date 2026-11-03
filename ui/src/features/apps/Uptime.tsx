import { useEffect, useState } from 'react'
import type { Workspace } from '../../api'
import { untilNextChange, uptime } from '../../lib/uptime'
import { readySince } from './startup'

const EXACT = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' })

/** The clock, re-read each time the text shown for `since` can change; only while mounted and `since` is set. */
function useNow(since: number | undefined): number {
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    if (since === undefined) return
    const id = setTimeout(() => setNow(Date.now()), untilNextChange(Date.now() - since))
    return () => clearTimeout(id)
  }, [since, now])
  return now
}

/**
 * "· 2 h 14 min" for a running app: how long since its current run became
 * ready (the API's Ready step time, which a restart resets). Nothing for an
 * app that is not running or has no Ready time.
 */
export function Uptime({ ws }: { ws: Workspace }) {
  const since = ws.phase === 'Running' ? readySince(ws.steps ?? []) : undefined
  const now = useNow(since)
  if (since === undefined) return null
  const at = new Date(since)
  return (
    <span className="uptime muted small">
      <span aria-hidden="true">· </span>
      <time dateTime={at.toISOString()} title={`Running since ${EXACT.format(at)}`}>
        {uptime(now - since)}
      </time>
    </span>
  )
}
