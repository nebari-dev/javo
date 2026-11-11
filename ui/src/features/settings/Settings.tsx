import { Check, Copy, ExternalLink, Plus, Trash2 } from 'lucide-react'
import { useEffect, useId, useRef, useState, type FormEvent } from 'react'
import { NavLink, Outlet, useLocation, useSearchParams } from 'react-router'
import type { CreatedToken, TokenView } from '../../api'
import { AgentDeploy } from '../../components/AgentDeploy'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { Confirm } from '../../components/Confirm'
import { EmptyState } from '../../components/EmptyState'
import { More } from '../../components/More'
import { PartIcon, PartTitle, type Part } from '../../components/Section'
import { Segmented } from '../../components/Segmented'
import { fieldErrors, problemText } from '../../lib/http'
import { useCreateToken, useMe, useRevokeToken, useServerInfo, useTokens } from '../../lib/queries'
import { useIsAdmin } from './admin/queries'

const SCOPES = [
  { value: 'read', label: 'Read' },
  { value: 'propose', label: 'Propose' },
  { value: 'read-write', label: 'Read and write' },
  { value: 'admin', label: 'Admin' },
] as const

type Scope = (typeof SCOPES)[number]['value']

/** The longest an admin token may live, as the server enforces it. */
const ADMIN_MAX_DAYS = 30

const SCOPE_HINTS: Record<Scope, string> = {
  read: 'Read can only look.',
  propose: 'Propose can look and propose changes that you approve in the browser: the one to give a coding agent over MCP.',
  'read-write': 'Read and write can also start, change and delete things, as a member: full access to your apps, jobs and files.',
  admin: 'Admin is read and write with your admin roles. It must expire within 30 days.',
}

const EXPIRIES = [
  { value: '7', label: '7 days' },
  { value: '30', label: '30 days' },
  { value: '90', label: '90 days' },
  { value: '365', label: '1 year' },
]

const NEVER = 'never'
const FORM_FIELDS = new Set(['name', 'scope', 'expiresInDays'])

const scopeLabel = (s: TokenView['scope']) => SCOPES.find((o) => o.value === s)?.label ?? s

/** "2 Nov 2026"; "Never" when the time is absent. */
function day(iso?: string): string {
  if (!iso) return 'Never'
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? iso : new Intl.DateTimeFormat('en-GB', { day: 'numeric', month: 'short', year: 'numeric' }).format(d)
}

function FieldError({ id, msg }: { id: string; msg?: string }) {
  return msg ? (
    <span id={id} className="errmsg">
      {msg}
    </span>
  ) : null
}

function TokenForm({ onCreated, onCancel, maxDays }: { onCreated: (t: CreatedToken) => void; onCancel: () => void; maxDays: number }) {
  const create = useCreateToken()
  const roles = useMe().data?.roles ?? []
  const admin = roles.includes('platform-admin') || roles.includes('template-admin')
  const scopes = SCOPES.filter((o) => o.value !== 'admin' || admin)
  const ids = { name: useId(), nameErr: useId(), scope: useId(), scopeErr: useId(), exp: useId(), expErr: useId() }
  const [name, setName] = useState('')
  const [scope, setScope] = useState<Scope>('read')
  const [expiry, setExpiry] = useState('30')
  // The server's limit, and 30 days for an admin token; 0 is none.
  const limit = scope === 'admin' ? (maxDays > 0 ? Math.min(maxDays, ADMIN_MAX_DAYS) : ADMIN_MAX_DAYS) : maxDays
  const expiries = EXPIRIES.filter((o) => limit === 0 || Number(o.value) <= limit)
  const chosen = expiry === NEVER ? (limit === 0 ? NEVER : '30') : expiries.some((o) => o.value === expiry) ? expiry : (expiries.at(-1)?.value ?? '7')
  const errs = create.isError ? fieldErrors(create.error) : { fields: {} as Record<string, string>, other: [] as string[] }
  const other = [...errs.other, ...Object.entries(errs.fields).filter(([f]) => !FORM_FIELDS.has(f)).map(([, m]) => m)]

  const submit = (e: FormEvent) => {
    e.preventDefault()
    if (!name.trim()) return
    create.mutate(
      { name: name.trim(), scope, ...(chosen === NEVER ? {} : { expiresInDays: Number(chosen) }) },
      { onSuccess: onCreated },
    )
  }

  return (
    <Card className="pad tok-form">
      <form onSubmit={submit} noValidate>
        <PartTitle part="tokens" as="h3" className="card-h">
          New token
        </PartTitle>
        {other.length > 0 && (
          <div className="field">
            <Banner tone="err" title={other.join(' ')} />
          </div>
        )}
        <div className="field">
          <label htmlFor={ids.name}>Name</label>
          <input
            id={ids.name}
            className={`input${errs.fields.name ? ' err' : ''}`}
            value={name}
            placeholder="laptop"
            maxLength={64}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={errs.fields.name ? true : undefined}
            aria-describedby={errs.fields.name ? ids.nameErr : undefined}
          />
          <FieldError id={ids.nameErr} msg={errs.fields.name} />
        </div>
        <div className="field" role="group" aria-labelledby={ids.scope}>
          <span id={ids.scope} className="field-label">
            Access
          </span>
          <Segmented label="Scope" value={scope} options={[...scopes]} onChange={setScope} />
          <span className="hint">{SCOPE_HINTS[scope]}</span>
          <FieldError id={ids.scopeErr} msg={errs.fields.scope} />
        </div>
        <div className="field" role="group" aria-labelledby={ids.exp}>
          <span id={ids.exp} className="field-label">
            Expires
          </span>
          <Segmented label="Expires after" value={chosen === NEVER ? '' : chosen} options={expiries} onChange={setExpiry} />
          {limit === 0 && (
            <More closed="Create a token that never expires" open="Hide the never-expires choice" defaultOpen={chosen === NEVER}>
              <div className="tok-never">
                <Segmented label="No expiry" value={chosen === NEVER ? NEVER : ''} options={[{ value: NEVER, label: 'Never' }]} onChange={setExpiry} />
                <span className="hint">A token that never expires works until you revoke it.</span>
              </div>
            </More>
          )}
          <FieldError id={ids.expErr} msg={errs.fields.expiresInDays} />
        </div>
        <div className="row tok-form-act">
          <Button variant="ghost" onClick={onCancel}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={!name.trim() || create.isPending}>
            Create token
          </Button>
        </div>
      </form>
    </Card>
  )
}

/** The new token's secret, shown this one time only. */
function NewSecret({ token, onDone }: { token: CreatedToken; onDone: () => void }) {
  const [copied, setCopied] = useState(false)
  const copy = () => {
    void navigator.clipboard.writeText(token.token).then(() => setCopied(true))
  }
  return (
    <div className="tok-secret" role="status">
      <h3>Token {token.name} is ready</h3>
      <div className="tok-secret-row">
        <code className="tok-secret-val">{token.token}</code>
        <Button size="sm" icon={copied ? Check : Copy} onClick={copy}>
          {copied ? 'Copied' : 'Copy'}
        </Button>
      </div>
      <p className="tight small">
        <b>You will not see this token again.</b> Store it now, for example in a password manager or your CI secrets.
      </p>
      <div>
        <Button variant="primary" onClick={onDone}>
          Done
        </Button>
      </div>
    </div>
  )
}

function TokenRow({ t }: { t: TokenView }) {
  const revoke = useRevokeToken(t.id)
  const [asking, setAsking] = useState(false)
  const btn = useRef<HTMLButtonElement>(null)
  const body = `Anything using it stops working at once.${t.app ? ` The app ${t.app} gets a new token when it next starts.` : ''}`
  return (
    <li className="tok-row">
      <div className="tok-main">
        <div className="tok-title">
          <h3 className="tok-name">{t.name}</h3>
          <span className="tok-scope">{scopeLabel(t.scope)}</span>
        </div>
        <span className="mono tok-prefix">{t.prefix}</span>
        {t.app && <span className="faint small">Used by app {t.app}</span>}
      </div>
      <dl className="tok-facts">
        <div>
          <dt>Created</dt>
          <dd>{day(t.createdAt)}</dd>
        </div>
        <div>
          <dt>Last used</dt>
          <dd>{day(t.lastUsedAt)}</dd>
        </div>
        <div>
          <dt>Expires</dt>
          <dd>{day(t.expiresAt)}</dd>
        </div>
      </dl>
      <div className="tok-act">
        <Button ref={btn} size="sm" variant="ghost" icon={Trash2} aria-label={`Revoke ${t.name}`} onClick={() => setAsking(true)}>
          <span className="tok-act-label">Revoke</span>
        </Button>
      </div>
      {asking && (
        <Confirm
          title={`Revoke ${t.name}?`}
          body={body}
          confirm="Revoke"
          icon={Trash2}
          danger
          busy={revoke.isPending}
          error={revoke.isError ? revoke.error : undefined}
          onConfirm={() => revoke.mutate()}
          onCancel={() => {
            setAsking(false)
            btn.current?.focus()
          }}
        />
      )}
    </li>
  )
}

/** Settings, API tokens: the index page every signed-in person sees. */
export function ApiTokens() {
  const tokens = useTokens()
  const info = useServerInfo()
  const [creating, setCreating] = useState(false)
  // ?token=new (the palette's New API token) opens the form.
  const [params] = useSearchParams()
  const askNew = params.get('token') === 'new'
  useEffect(() => {
    if (askNew) setCreating(true)
  }, [askNew])
  // The secret lives only here, until Done; it never enters the list's cache.
  const [made, setMade] = useState<CreatedToken | null>(null)
  const list = tokens.data ?? []
  return (
    <section className="section" aria-label="API tokens">
      <div className="section-head tok-head">
        <p className="sub tok-intro">
          A token lets scripts and coding agents use the API as you. Send it as <code>Authorization: Bearer &lt;token&gt;</code>.
        </p>
        <div className="right row">
          {info.data?.apiDocs && (
            <a className="btn" href="/api/docs/" target="_blank" rel="noopener">
              <ExternalLink size={16} aria-hidden="true" />
              API docs
            </a>
          )}
          <Button icon={Plus} disabled={creating || !!made} onClick={() => setCreating(true)}>
            New token
          </Button>
        </div>
      </div>
      <div className="section-sm">
        <AgentDeploy />
      </div>
      {made ? (
        <div className="section-sm">
          <NewSecret token={made} onDone={() => setMade(null)} />
        </div>
      ) : (
        creating && (
          <div className="section-sm">
            <TokenForm
              maxDays={info.data?.tokenMaxDays ?? 0}
              onCreated={(t) => {
                setCreating(false)
                setMade(t)
              }}
              onCancel={() => setCreating(false)}
            />
          </div>
        )
      )}
      <div className="section-sm">
        {tokens.isError ? (
          <Banner tone="err" title={problemText(tokens.error)} />
        ) : tokens.isPending ? (
          <div className="muted">Loading</div>
        ) : list.length === 0 ? (
          <EmptyState title="No API tokens yet" body="Make one with New token to use the API from a script or a coding agent." />
        ) : (
          <Card>
            <ul className="tok-list" aria-label="API tokens">
              {list.map((t) => (
                <TokenRow key={t.id} t={t} />
              ))}
            </ul>
          </Card>
        )}
      </div>
    </section>
  )
}

/** Admin pages, for platform admins only. */
const ADMIN: { to: string; label: string; icon: Part }[] = [
  { to: 'apps', label: 'All apps', icon: 'apps' },
  { to: 'usage', label: 'Usage', icon: 'usage' },
  { to: 'backups', label: 'Backups', icon: 'backups' },
  { to: 'storage', label: 'Storage', icon: 'storage' },
  { to: 'ai', label: 'AI', icon: 'ai' },
  { to: 'agents', label: 'Agents', icon: 'trust' },
]

/** Settings: API tokens and secrets, and for platform admins the admin pages beside them. */
export function Settings() {
  const admin = useIsAdmin()
  const nav = useRef<HTMLElement>(null)
  const { pathname } = useLocation()
  // On a narrow screen the sections scroll sideways: keep the open one in view.
  useEffect(() => {
    nav.current?.querySelector('a.active')?.scrollIntoView?.({ block: 'nearest', inline: 'nearest' })
  }, [pathname, admin])
  return (
    <>
      <h1>Settings</h1>
      <nav ref={nav} className="tablist subnav" aria-label="Settings sections">
        <NavLink to="/settings" end>
          <PartIcon part="tokens" size={16} />
          API tokens
        </NavLink>
        <NavLink to="/settings/secrets">
          <PartIcon part="secrets" size={16} />
          Secrets
        </NavLink>
        {admin &&
          ADMIN.map((a) => (
            <NavLink key={a.to} to={`/settings/${a.to}`}>
              <PartIcon part={a.icon} size={16} />
              {a.label}
            </NavLink>
          ))}
      </nav>
      <Outlet />
    </>
  )
}
