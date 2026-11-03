import { ExternalLink, GitPullRequest } from 'lucide-react'
import { Link } from 'react-router'
import type { PreviewItemView, PreviewsView, Workspace } from '../../api'
import { ago } from '../../lib/ago'
import { problemText } from '../../lib/http'
import { useMakeWebhook, usePreviewApps, usePreviews, useSetPreviews } from '../../lib/queries'
import { appLink, pillStatus } from './status'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { CodeBlock } from '../../components/CodeBlock'
import { More } from '../../components/More'
import { Pill } from '../../components/Pill'
import { Select } from '../../components/Select'
import { Switch } from '../../components/Switch'

function PullRequestRow({ item, app }: { item: PreviewItemView; app: Workspace | undefined }) {
  const title = `#${item.number} ${item.title}`
  if (!item.app) {
    return (
      <li className="prow">
        <div className="ptitle">{title}</div>
        {item.text && <div className="muted small">{item.text}</div>}
      </li>
    )
  }
  const open = app?.phase === 'Running' ? appLink(app.url) : undefined
  return (
    <li className="prow">
      <Link className="ptitle" to={`/apps/${encodeURIComponent(app?.name ?? item.app)}`}>
        {title}
      </Link>
      <div className="row">
        {app && <Pill status={pillStatus(app.phase)} label={app.phase} />}
        {open && (
          <a className="btn sm" href={open} aria-label={`Open the preview of pull request #${item.number}`}>
            <ExternalLink size={16} aria-hidden="true" />
            Open
          </a>
        )}
      </div>
    </li>
  )
}

/** A webhook address and secret, in the one answer that shows the secret. */
interface ShownWebhook {
  url: string
  secret: string
  notice: string
}

/** The webhook part of the preview settings: made (or replaced) on request, the secret shown once. */
interface WebhookState {
  /** The app already has one (whose secret is not shown again). */
  configured: boolean
  shown?: ShownWebhook
  pending: boolean
  error?: unknown
  make: () => void
}

/**
 * True for the address of a repository on github.com, the only host previews
 * work with: what the API's own check (model.GitHubRepo) takes. It only
 * decides whether the form offers previews; the API decides the rest.
 */
export function isGitHubRepo(raw: string): boolean {
  let u: URL
  try {
    u = new URL(raw.trim())
  } catch {
    return false
  }
  if (u.protocol !== 'https:' || u.hostname.toLowerCase() !== 'github.com' || u.port) return false
  const parts = u.pathname.replace(/^\/+|\/+$/g, '').split('/')
  const repo = (parts[1] ?? '').replace(/\.git$/, '')
  return parts.length === 2 && /^[A-Za-z0-9][A-Za-z0-9-]{0,38}$/.test(parts[0]) && /^[A-Za-z0-9._-]{1,100}$/.test(repo) && repo !== '.' && repo !== '..'
}

/**
 * Limit, forks and the webhook, shared by the app page (each saves at once)
 * and the New app form (sent with the app).
 */
export function PreviewSettings({
  id,
  max,
  maxAllowed,
  forks,
  forksWarning,
  onChange,
  webhook,
}: Pick<PreviewsView, 'max' | 'maxAllowed' | 'forks' | 'forksWarning'> & {
  id: string
  onChange: (patch: Partial<Pick<PreviewsView, 'max' | 'forks'>>) => void
  webhook: WebhookState
}) {
  const { configured, shown } = webhook
  return (
    <div className="stack">
      <div className="field tight">
        <label htmlFor={`${id}-previews-max`}>Previews at most</label>
        <Select id={`${id}-previews-max`} value={max} onValueChange={(v) => onChange({ max: Number(v) })}>
          {Array.from({ length: maxAllowed }, (_, i) => i + 1).map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </Select>
        <span className="hint">The newest open pull requests get one; the others are listed with the reason.</span>
      </div>
      <div className="field tight">
        <Switch label="Also preview pull requests from forks" checked={forks} onChange={(f) => onChange({ forks: f })} />
        <span className="hint">{forksWarning}</span>
      </div>
      <div className="field tight">
        <span className="field-label">{configured || shown ? 'Webhook: set up' : 'Webhook: not set up'}</span>
        <span className="hint">
          Without one, pull requests are checked every few minutes. A webhook on the repository makes a new pull request appear at once.
        </span>
        {configured && !shown && (
          <span className="hint">A new address and secret replace the current ones. The secret now on GitHub stops working until you paste the new one there.</span>
        )}
        {shown && (
          <div className="stack">
            <CodeBlock text={shown.url} label="Copy webhook address" />
            <CodeBlock text={shown.secret} label="Copy webhook secret" />
            <span className="hint">{shown.notice}</span>
          </div>
        )}
        {webhook.error !== undefined && <span className="errmsg">{problemText(webhook.error)}</span>}
        <div>
          <Button size="sm" disabled={webhook.pending} onClick={webhook.make}>
            {configured || shown ? 'Replace webhook address and secret' : 'Set up webhook'}
          </Button>
        </div>
      </div>
    </div>
  )
}

/** Limit, forks and the webhook of an app: each saves at once. */
function Settings({ name, view, webhookConfigured }: { name: string; view: PreviewsView; webhookConfigured: boolean }) {
  const set = useSetPreviews(name)
  const hook = useMakeWebhook(name)
  return (
    <div className="stack">
      <PreviewSettings
        id={name}
        max={view.max}
        maxAllowed={view.maxAllowed}
        forks={view.forks}
        forksWarning={view.forksWarning}
        onChange={(patch) => set.mutate({ enabled: view.enabled, max: view.max, forks: view.forks, ...patch })}
        webhook={{ configured: webhookConfigured, shown: hook.data, pending: hook.isPending, error: hook.isError ? hook.error : undefined, make: () => hook.mutate() }}
      />
      {set.isError && <span className="errmsg">{problemText(set.error)}</span>}
    </div>
  )
}

/** One preview app per open pull request of a GitHub repository app: off, or on with the pull requests and their previews. */
export function PreviewsCard({ ws }: { ws: Workspace }) {
  const q = usePreviews(ws.name)
  const set = useSetPreviews(ws.name)
  const view = q.data
  const apps = usePreviewApps(ws.name, !!view?.enabled)
  return (
    <Card className="pad">
      <div className="label">
        <GitPullRequest size={15} aria-hidden="true" />
        Previews
      </div>
      {q.isError ? (
        <Banner tone="err" title={problemText(q.error)} />
      ) : !view ? (
        <div className="muted">Loading</div>
      ) : (
        <div className="stack">
          <div className="stack tight-gap">
            <Switch
              label="Preview open pull requests"
              checked={view.enabled}
              onChange={(enabled) => set.mutate({ enabled, max: view.max, forks: view.forks })}
            />
            <p className="tight muted small">Each open pull request gets its own app and link. It follows new commits and goes away when the pull request closes.</p>
            {set.isError && <span className="errmsg">{problemText(set.error)}</span>}
          </div>
          {view.enabled && (
            <>
              {view.reason && (
                <Banner tone="warn" title={view.title}>
                  {view.text}
                  {view.todo && <p className="tight">{view.todo}</p>}
                </Banner>
              )}
              {(view.items ?? []).length > 0 ? (
                <ul className="plist" aria-label="Pull requests">
                  {(view.items ?? []).map((item) => (
                    <PullRequestRow key={item.number} item={item} app={apps.data?.get(item.number)} />
                  ))}
                </ul>
              ) : (
                !view.reason && <p className="tight muted">No open pull requests.</p>
              )}
              {view.syncedAt && <p className="tight muted small">Checked {ago(view.syncedAt)}.</p>}
            </>
          )}
          <More closed="Limit, pull requests from forks and webhook" open="Hide limit, forks and webhook">
            <Settings name={ws.name} view={view} webhookConfigured={!!ws.source?.webhookConfigured} />
          </More>
        </div>
      )}
    </Card>
  )
}
