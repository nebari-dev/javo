import { LogIn } from 'lucide-react'
import { useEffect, useState, type FormEvent } from 'react'
import { useLocation, useNavigate, useSearchParams } from 'react-router'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { ThemeSwitch } from '../../components/ThemeSwitch'
import { problemText } from '../../lib/http'
import type { LoginOption } from '../../api'
import { BrandLogo, BrandName } from '../../components/BrandLogo'
import { safeNext } from '../../lib/localPath'
import { useAuthOptions, useLogin, useReasonCopy } from '../../lib/queries'


export interface LoginState {
  expired?: boolean
}

function startHref(o: LoginOption, next: string): string {
  const start = o.startURL ?? ''
  return next === '/' ? start : `${start}?next=${encodeURIComponent(next)}`
}

function ProviderProblem({ option }: { option: LoginOption }) {
  const r = useReasonCopy(option.reason).data
  if (!r) return null
  return (
    <Banner tone="err" title={r.title}>
      {r.what} {r.todo}
    </Banner>
  )
}

export function Login() {
  const [params] = useSearchParams()
  const { state } = useLocation() as { state: LoginState | null }
  const navigate = useNavigate()
  const signIn = useLogin()
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const next = safeNext(params.get('next'))
  const options = useAuthOptions()
  const list = options.data ?? []
  const redirects = list.filter((o) => o.kind === 'redirect')
  const working = redirects.filter((o) => !o.disabled)
  // Without a usable options answer the password form stays, so sign-in is still possible.
  const showForm = options.isError || list.some((o) => o.kind === 'password')
  const linkError = useReasonCopy(params.get('error') === 'SignInLinkExpired' ? 'SignInLinkExpired' : null).data
  const signedOut = params.get('signedOut') === '1'
  // One working provider and no password form: go to it at once. The
  // provider's own session signs the person in without a click. Not right
  // after signing out (the provider would sign them straight back in), and
  // not while a problem is shown (that could loop).
  const only = !showForm && working.length === 1 && redirects.length === 1 ? working[0] : undefined
  const auto = !!only && !signedOut && !params.get('error') && !state?.expired
  useEffect(() => {
    if (auto && only) window.location.assign(startHref(only, next))
  }, [auto, only, next])

  const submit = (e: FormEvent) => {
    e.preventDefault()
    signIn.mutate(
      { username, password },
      {
        onSuccess: () => {
          // App links (/u/...) and the app ticket route (/api/...) are served
          // by the gateway, not this app, so they need a full page load.
          if (next.startsWith('/u/') || next.startsWith('/api/')) window.location.assign(next)
          else navigate(next, { replace: true })
        },
      },
    )
  }

  return (
    <div className="login">
      <div className="login-top">
        <ThemeSwitch />
      </div>
      <Card className="pad login-card">
        <div className="brand">
          <BrandLogo />
          <BrandName />
        </div>
        <h1>Sign in</h1>
        {signedOut && <Banner tone="info" title="You signed out." />}
        {state?.expired && !signIn.isError && (
          <Banner tone="info" title="Your session ended. Sign in again." />
        )}
        {linkError && (
          <Banner
            tone="err"
            title={linkError.title}
            actions={
              working[0] && (
                <Button size="sm" onClick={() => window.location.assign(startHref(working[0], next))}>
                  Start again
                </Button>
              )
            }
          >
            {linkError.what} {linkError.todo}
          </Banner>
        )}
        {options.isError && <Banner tone="err" title={problemText(options.error)} />}
        {redirects.filter((o) => o.disabled).map((o) => (
          <ProviderProblem key={o.id} option={o} />
        ))}
        {signIn.isError && <Banner tone="err" title={problemText(signIn.error)} />}
        {working.map((o) => (
          <a key={o.id} className={`btn ${showForm ? '' : 'primary '}wide`} href={startHref(o, next)}>
            <LogIn size={16} aria-hidden="true" />
            {o.label}
          </a>
        ))}
        {working.length > 0 && showForm && <div className="login-or faint small">or</div>}
        {options.isPending && <div className="muted">Loading</div>}
        {showForm && (
        <form onSubmit={submit} className="login-form">
          <div className="field">
            <label htmlFor="username">Username</label>
            <input
              id="username"
              className="input"
              autoComplete="username"
              autoFocus
              required
              value={username}
              onChange={(e) => setUsername(e.target.value)}
            />
          </div>
          <div className="field">
            <label htmlFor="password">Password</label>
            <input
              id="password"
              className="input"
              type="password"
              autoComplete="current-password"
              required
              value={password}
              onChange={(e) => setPassword(e.target.value)}
            />
          </div>
          <Button type="submit" variant="primary" icon={LogIn} className="wide" disabled={signIn.isPending}>
            Sign in
          </Button>
        </form>
        )}
      </Card>
    </div>
  )
}
