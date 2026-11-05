import { ArrowDown, ScrollText, WrapText } from 'lucide-react'
import { useId, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { plainText } from './ansi'
import { CopyButton } from '../../components/CopyButton'
import { IconButton } from '../../components/IconButton'
import { MoreToggle } from '../../components/More'
import { LogLines, type LogLine } from './LogLines'

/** Within this many pixels of the bottom counts as following the newest line. */
const AT_BOTTOM_PX = 8

/** Where the lines come from, said in the header. */
type LogStatus = 'live' | 'saved' | 'previous'

const STATUS_TEXT: Record<LogStatus, string> = { live: 'Live', saved: 'Saved log', previous: 'Previous run' }

interface LogViewProps {
  /** What the log is, shown in the header ("App log", "Run log"). */
  title: string
  /** The accessible name of the list of lines. */
  label: string
  lines: LogLine[]
  /** What to say when there are no lines. */
  empty: { title: string; body: string }
  status?: LogStatus
  /** Keep the newest line in view until the user scrolls up. */
  follow?: boolean
  loading?: boolean
  /** Why the lines cannot be shown; replaces them. */
  error?: string
  /** More header actions, as IconButtons, placed before the standard ones. */
  actions?: ReactNode
  /** A shorter body, for logs inside another list (startup steps). */
  compact?: boolean
  /** Stands on the page as a card of its own, its title a heading. */
  card?: boolean
  /** Shown above the lines and kept there as they scroll (what the app started with); not part of the copied log. */
  top?: ReactNode
  /** Folds to its header, whose title is then the toggle; the header stays put in both states. */
  fold?: { open: boolean; onToggle: () => void }
}

function lineCount(n: number): string {
  return n === 1 ? '1 line' : `${n.toLocaleString()} lines`
}

/**
 * The one log viewer: a framed surface with a header bar (what the log is,
 * where it comes from, how many lines, and icon actions: copy, wrap, jump to
 * latest), then the highlighted lines under a time or line number gutter.
 */
export function LogView({ title, label, lines, empty, status, follow = false, loading = false, error, actions, compact = false, card = false, top, fold }: LogViewProps) {
  const [following, setFollowing] = useState(true)
  const [wrap, setWrap] = useState(true)
  const box = useRef<HTMLDivElement>(null)
  const titleId = useId()
  const bodyId = useId()

  useLayoutEffect(() => {
    const el = box.current
    if (el && follow && following) el.scrollTop = el.scrollHeight
  }, [lines, follow, following])

  const jump = () => {
    setFollowing(true)
    const el = box.current
    if (el) el.scrollTop = el.scrollHeight
  }

  const shown = !loading && !error && lines.length > 0
  const folded = fold !== undefined && !fold.open
  const icon = <ScrollText size={16} aria-hidden="true" className="lv-icon" />
  const heading = fold ? (
    <MoreToggle className="lv-toggle" open={fold.open} onToggle={fold.onToggle} controls={fold.open ? bodyId : undefined}>
      {icon}
      {title}
    </MoreToggle>
  ) : (
    title
  )
  return (
    <section className={['lv', compact ? 'compact' : '', card ? 'card-lv' : '', folded ? 'folded' : ''].filter(Boolean).join(' ')} aria-labelledby={titleId}>
      <header className="lv-head">
        {!fold && icon}
        {card ? (
          <h2 id={titleId} className="lv-title">
            {heading}
          </h2>
        ) : (
          <span id={titleId} className="lv-title">
            {heading}
          </span>
        )}
        {!folded && status && (
          <span className={`lv-status ${status}`}>
            {status === 'live' && <span className="lv-dot" aria-hidden="true" />}
            {STATUS_TEXT[status]}
          </span>
        )}
        {!folded && shown && <span className="lv-count">{lineCount(lines.length)}</span>}
        {!folded && (
          <span className="lv-actions">
            {follow && !following && <IconButton icon={ArrowDown} label="Jump to latest" className="lv-jump" onClick={jump} />}
            {actions}
            <IconButton icon={WrapText} label="Wrap lines" pressed={wrap} onClick={() => setWrap(!wrap)} />
            <CopyButton label="Copy log" disabled={!shown} text={() => lines.map((l) => plainText(l.text)).join('\n')} />
          </span>
        )}
      </header>
      {!folded && (
        <>
          {top && <div className="lv-top">{top}</div>}
          <div
            ref={box}
            id={bodyId}
            className="lv-body"
            role="log"
            data-wrap={wrap ? 'on' : 'off'}
            aria-live={follow && status === 'live' ? 'polite' : 'off'}
            aria-busy={loading || undefined}
            tabIndex={0}
            onScroll={(e) => {
              if (!follow) return
              const el = e.currentTarget
              setFollowing(el.scrollHeight - el.scrollTop - el.clientHeight <= AT_BOTTOM_PX)
            }}
          >
            {loading ? (
              <div className="lv-loading">
                <span className="lv-skel" aria-hidden="true" />
                <span className="lv-skel short" aria-hidden="true" />
                <span className="lv-skel mid" aria-hidden="true" />
                <span className="lv-note">Loading the log</span>
              </div>
            ) : error ? (
              <div className="lv-empty">
                <ScrollText size={20} aria-hidden="true" className="lv-empty-icon" />
                <p className="lv-empty-title">No log to show</p>
                <p className="lv-empty-body">{error}</p>
              </div>
            ) : shown ? (
              <LogLines label={label} lines={lines} />
            ) : (
              <div className="lv-empty">
                <ScrollText size={20} aria-hidden="true" className="lv-empty-icon" />
                <p className="lv-empty-title">{empty.title}</p>
                <p className="lv-empty-body">{empty.body}</p>
              </div>
            )}
          </div>
        </>
      )}
    </section>
  )
}
