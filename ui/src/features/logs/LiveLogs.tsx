import { History } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import type { LogLine as ApiLogLine } from '../../api'
import { problemText } from '../../lib/http'
import { useWorkspaceLogs } from '../../lib/queries'
import { IconButton } from '../../components/IconButton'
import type { LogLine } from './LogLines'
import { LogView } from './LogView'

/** How many streamed lines are kept; older ones drop off the top. */
const MAX_LINES = 2000

function streamURL(name: string): string {
  return `/api/workspaces/${encodeURIComponent(name)}/logs/stream`
}

/**
 * The app's log, live. The server replays the latest lines on every
 * (re)connect, so each open starts from an empty list. The view follows the
 * newest line until the user scrolls up, and "Jump to latest" resumes it.
 * Folded, it is just its header and opens no stream.
 */
export function LiveLogs({ name, restarts, title = 'Logs', top, fold }: { name: string; restarts: number; title?: string; top?: ReactNode; fold?: { open: boolean; onToggle: () => void } }) {
  const [lines, setLines] = useState<LogLine[]>([])
  const [previous, setPrevious] = useState(false)
  const open = !fold || fold.open
  const prev = useWorkspaceLogs(name, { tail: 500, previous: true, enabled: open && previous })

  useEffect(() => {
    if (!open) return
    const es = new EventSource(streamURL(name))
    es.onopen = () => setLines([])
    let seq = 0
    const onLine = (e: MessageEvent) => {
      const l: LogLine = { ...(JSON.parse(e.data) as ApiLogLine), id: ++seq }
      setLines((cur) => (cur.length >= MAX_LINES ? [...cur.slice(cur.length - MAX_LINES + 1), l] : [...cur, l]))
    }
    es.addEventListener('line', onLine)
    return () => {
      es.removeEventListener('line', onLine)
      es.close()
    }
  }, [name, open])

  return (
    <LogView
      card
      fold={fold}
      top={top}
      title={title}
      label={previous ? 'Previous run log' : 'App log'}
      status={previous ? 'previous' : 'live'}
      follow={!previous}
      lines={previous ? (prev.data?.lines ?? []) : lines}
      loading={previous && prev.isPending}
      error={previous && prev.isError ? problemText(prev.error) : undefined}
      empty={
        previous
          ? { title: 'No log lines', body: 'The run before the last restart wrote nothing.' }
          : { title: 'No log lines yet', body: 'Lines the app writes show up here as they happen.' }
      }
      actions={restarts > 0 && <IconButton icon={History} label="Show previous run" pressed={previous} onClick={() => setPrevious(!previous)} />}
    />
  )
}
