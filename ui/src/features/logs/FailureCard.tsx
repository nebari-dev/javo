import type { ReactNode } from 'react'
import { Card } from '../../components/Card'
import { LogView } from './LogView'
import { More } from '../../components/More'
import { PartIcon, type Part } from '../../components/Section'

export interface FailureCardProps {
  title: ReactNode
  what: ReactNode
  todo: ReactNode
  blame: ReactNode
  who: ReactNode
  evidence?: string[]
  /** A button that applies the fix, shown under What you can do. */
  action?: ReactNode
  /** Output that says what went wrong (a build's error lines), shown in the log viewer instead of behind the disclosure. */
  log?: { title: string; lines: string[] }
  /** info for something the platform is doing (nothing failed); err by default. */
  tone?: 'info' | 'err'
}

// Never shown: the card's log view appears only with lines.
const NO_LINES = { title: 'No lines', body: '' }

/** A part of the card: an icon and a name over its text, so the name reads as a heading. */
function Labelled({ part, label, children }: { part: Part; label: string; children: ReactNode }) {
  return (
    <div>
      <div className="k">
        <PartIcon part={part} />
        {label}
      </div>
      <div className="fail-body">{children}</div>
    </div>
  )
}

export function FailureCard({ title, what, todo, blame, who, evidence = [], action, log, tone = 'err' }: FailureCardProps) {
  return (
    <Card className={`pad failure${tone === 'info' ? ' info' : ''}`} role={tone === 'info' ? 'status' : 'alert'}>
      <h3>{title}</h3>
      <div className="fail-grid">
        <Labelled part="cause" label="What happened">
          {what}
        </Labelled>
        <Labelled part="fix" label="What you can do">
          {todo}
          {action && <div className="fail-action">{action}</div>}
        </Labelled>
      </div>
      {log && log.lines.length > 0 && (
        <div className="fail-log">
          <LogView compact title={log.title} label={`${log.title} lines`} empty={NO_LINES} lines={log.lines.map((text) => ({ text }))} />
        </div>
      )}
      <div className="fail-more">
        <More
          closed={log ? 'Who can help and whose fault it is' : 'Who can help, whose fault it is, and the evidence'}
          open={log ? 'Hide who can help and whose fault it is' : 'Hide who can help, whose fault it is, and the evidence'}
        >
          <div className="fail-grid">
            <Labelled part="blame" label="Is it you or the platform?">
              {blame}
            </Labelled>
            <Labelled part="help" label="Who can help">
              {who}
            </Labelled>
          </div>
          {!log && evidence.length > 0 && (
            <div className="fail-evidence">
              <div className="k">
                <PartIcon part="evidence" />
                Evidence
              </div>
              <pre className="evidence">{evidence.join('\n')}</pre>
            </div>
          )}
        </More>
      </div>
    </Card>
  )
}
