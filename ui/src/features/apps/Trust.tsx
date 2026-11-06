import { ShieldAlert } from 'lucide-react'
import { useState } from 'react'
import type { Workspace } from '../../api'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { Confirm } from '../../components/Confirm'
import { KindLogo } from '../../components/KindLogo'
import { PartIcon } from '../../components/Section'
import { Segmented } from '../../components/Segmented'
import { Switch } from '../../components/Switch'
import { brand } from '../../lib/brand'
import { problemText } from '../../lib/http'
import { useSetTrust } from '../../lib/queries'
import { AGENTS } from './agents'

// How long the owner may pick, in hours; the server allows 1 to 24 and picks 8.
const HOURS = ['1', '4', '8', '24'] as const
type Hours = (typeof HOURS)[number]
const DEFAULT_HOURS: Hours = '8'

const CLOCK = new Intl.DateTimeFormat('en-GB', { hour: '2-digit', minute: '2-digit' })

/** A time today or tomorrow as the page says it: "14:30" or "09:15 tomorrow". */
export function untilText(at: Date, now = new Date()): string {
  const tomorrow = new Date(now)
  tomorrow.setDate(now.getDate() + 1)
  const day = at.toDateString() === now.toDateString() ? '' : at.toDateString() === tomorrow.toDateString() ? ' tomorrow' : ''
  return CLOCK.format(at) + day
}

/** The agent's name as people know it ("Pi"), or "This agent" for a kind without one. */
const agentOf = (ws: Workspace) => AGENTS[ws.kind] ?? 'This agent'

/** When the agent acts without asking until, or undefined while it asks. */
function trustedUntil(ws: Workspace): Date | undefined {
  const until = ws.trust?.until
  return until ? new Date(until) : undefined
}

/** What acting without asking lets the agent do, in the same words wherever it is said. */
function canDo(): string {
  return `It can do anything you can do in ${brand().name}: start, stop and delete apps, read and attach your secrets, share apps.`
}

/** The warning before the agent may act without asking: what it means, the risk, for how long, and how to stop. */
function TrustWarning({ ws, busy, error, onConfirm, onCancel }: {
  ws: Workspace
  busy: boolean
  error: unknown
  onConfirm: (hours: number) => void
  onCancel: () => void
}) {
  const agent = agentOf(ws)
  const [hours, setHours] = useState<Hours>(DEFAULT_HOURS)
  const n = Number(hours)
  const until = untilText(new Date(Date.now() + n * 3600_000))
  return (
    <Confirm
      title={`Let ${agent} act without asking?`}
      body={`${agent} now asks you before changes it cannot make on its own. Turned on, it acts as you instead.`}
      details={
        <>
          <dl className="trust-terms">
            <dt>What it means</dt>
            <dd>{canDo()}</dd>
            <dt>The risk</dt>
            <dd>If a web page, file or package tricks {agent}, it can do those things for whoever tricked it.</dd>
            <dt>How long</dt>
            <dd>Until {until}, then it asks again.</dd>
            <dt>To stop</dt>
            <dd>Turn it off here any time.</dd>
          </dl>
          <Segmented
            label="For how long"
            value={hours}
            onChange={setHours}
            options={HOURS.map((h) => ({ value: h, label: h === '1' ? '1 hour' : `${h} hours` }))}
          />
        </>
      }
      confirm={`Let ${agent} act without asking for ${n === 1 ? '1 hour' : `${n} hours`}`}
      cancel="Keep asking"
      icon={ShieldAlert}
      danger
      busy={busy}
      error={error}
      onConfirm={() => onConfirm(n)}
      onCancel={onCancel}
    />
  )
}

/**
 * The switch on a coding agent's page that lets it act without asking. Turning
 * it on opens the warning; turning it off takes effect at once. Nothing for an
 * app that cannot be trusted (the API sends no trust).
 */
export function TrustCard({ ws }: { ws: Workspace }) {
  const set = useSetTrust(ws.name)
  const [asking, setAsking] = useState(false)
  if (!ws.trust) return null
  const agent = agentOf(ws)
  const until = trustedUntil(ws)
  return (
    <Card className="pad trust-card">
      <div className="label">
        <PartIcon part="trust" />
        Asking first
      </div>
      {ws.trust.allowed ? (
        <div className="field fit tight">
          <Switch
            label={`Let ${agent} act without asking`}
            checked={!!until}
            onChange={(on) => (on ? setAsking(true) : set.mutate(null))}
          />
          <span className="hint">
            {until
              ? `${agent} acts as you until ${untilText(until)}, without asking.`
              : `${agent} asks you to approve changes it cannot make on its own. Turn this on to let it act as you for a few hours.`}
          </span>
          {set.isError && !asking && <span className="errmsg">{problemText(set.error)}</span>}
        </div>
      ) : (
        <p className="tight muted small">Your admin turned this off.</p>
      )}
      {asking && (
        <TrustWarning
          ws={ws}
          busy={set.isPending}
          error={set.isError ? set.error : undefined}
          onConfirm={(hours) => set.mutate(hours, { onSuccess: () => setAsking(false) })}
          onCancel={() => {
            set.reset()
            setAsking(false)
          }}
        />
      )}
    </Card>
  )
}

/** The red banner on the agent's page while it acts without asking, with Turn off. */
export function TrustBanner({ ws }: { ws: Workspace }) {
  const set = useSetTrust(ws.name)
  const until = trustedUntil(ws)
  if (!until) return null
  const agent = agentOf(ws)
  return (
    <Banner
      tone="err"
      className="trust-banner"
      icon={<KindLogo kind={ws.kind} />}
      title={`${agent} acts without asking until ${untilText(until)}`}
      actions={
        <Button size="sm" variant="danger" disabled={set.isPending} onClick={() => set.mutate(null)}>
          Turn off
        </Button>
      }
    >
      {canDo()} {set.isError ? <span className="errmsg block">{problemText(set.error)}</span> : null}
    </Banner>
  )
}

/** The line on an agent's card and row in Apps while it acts without asking, with Turn off. */
export function TrustBadge({ ws }: { ws: Workspace }) {
  const set = useSetTrust(ws.name)
  const until = trustedUntil(ws)
  if (!until) return null
  return (
    <span className="trust-badge">
      <ShieldAlert size={14} aria-hidden="true" />
      <span className="grow">{`Acts without asking until ${untilText(until)}`}</span>
      <Button size="sm" disabled={set.isPending} onClick={() => set.mutate(null)}>
        Turn off
      </Button>
    </span>
  )
}
