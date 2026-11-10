import { useMutation, useQuery } from '@tanstack/react-query'
import { Check, CircleAlert, CircleCheck, X } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { approveDeviceLogin, denyDeviceLogin, getDeviceLogin } from '../../api'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { ApiError, problemText } from '../../lib/http'

const GRANTS = [
  'Create, change, start, stop and delete your apps and jobs',
  'Write files in your home folder',
  'Store and change your secrets',
  'Share your apps, including publicly',
  'Give an app its own API access, which lasts until you turn it off',
]

function hours(h: number): string {
  if (h < 1) return 'less than an hour'
  return h === 1 ? '1 hour' : `${h} hours`
}

function lookupProblem(err: unknown): string {
  if (err instanceof ApiError && err.status === 410) return 'This request has expired. Ask your agent to start again.'
  if (err instanceof ApiError && err.status === 404) return 'No request has this code. Check it and try again.'
  return problemText(err)
}

/** No code in the address: the person types what the agent showed. */
function CodeForm() {
  const navigate = useNavigate()
  const [code, setCode] = useState('')
  const go = (e: FormEvent) => {
    e.preventDefault()
    const c = code.trim().toUpperCase()
    if (c) navigate(`/device?code=${encodeURIComponent(c)}`)
  }
  return (
    <Card className="sheet device">
      <h1>Approve an agent login</h1>
      <form className="field" onSubmit={go}>
        <label htmlFor="device-code">Code shown by your agent</label>
        <input id="device-code" className="input mono" value={code} onChange={(e) => setCode(e.target.value)} autoComplete="off" autoCapitalize="characters" spellCheck={false} placeholder="XXXX-XXXX" />
        <div className="bar">
          <Button type="submit" variant="primary" disabled={!code.trim()}>
            Continue
          </Button>
        </div>
      </form>
    </Card>
  )
}

function Request({ code }: { code: string }) {
  const q = useQuery({
    queryKey: ['device', code],
    retry: false,
    queryFn: async () => (await getDeviceLogin({ path: { userCode: code }, throwOnError: true })).data,
  })
  const decide = useMutation({
    mutationFn: async (approve: boolean) => {
      const path = { userCode: code }
      if (approve) await approveDeviceLogin({ path, throwOnError: true })
      else await denyDeviceLogin({ path, throwOnError: true })
      return approve
    },
  })
  const result = decide.isSuccess ? (decide.data ? 'Approved. You can go back to your agent.' : 'Denied. The agent gets no access.') : ''
  const problem = q.isError ? lookupProblem(q.error) : decide.isError ? problemText(decide.error) : ''
  const earlier = q.data && q.data.state !== 'pending' ? 'This request was already answered.' : ''
  return (
    <Card className="sheet device">
      <h1>Approve an agent login</h1>
      {q.isPending ? (
        <div className="muted">Loading</div>
      ) : q.data ? (
        <>
          <p className="devcode">
            <code>{q.data.userCode}</code>
          </p>
          <p>An agent asked to act as you through the API for {hours(q.data.tokenHours)}. Approve only if you started this yourself just now.</p>
          <ul className="cons" aria-label="What the agent can do">
            {GRANTS.map((g) => (
              <li key={g} className="ok">
                <i>
                  <Check size={16} aria-hidden="true" />
                </i>
                <div>
                  <b>{g}</b>
                </div>
              </li>
            ))}
          </ul>
          {result ? (
            <p className="rq-done">
              <CircleCheck size={17} aria-hidden="true" />
              <span>{result}</span>
            </p>
          ) : earlier ? (
            <p className="rq-done">
              <span>{earlier}</span>
            </p>
          ) : (
            <div className="bar">
              <Button variant="primary" icon={Check} disabled={decide.isPending} onClick={() => decide.mutate(true)}>
                Approve
              </Button>
              <Button icon={X} disabled={decide.isPending} onClick={() => decide.mutate(false)}>
                Deny
              </Button>
            </div>
          )}
        </>
      ) : null}
      {problem && (
        <p className="rq-done bad" role="alert">
          <CircleAlert size={17} aria-hidden="true" />
          <span>{problem}</span>
        </p>
      )}
    </Card>
  )
}

/** The page an agent's login link opens: what the agent would get, and Approve or Deny. */
export function DevicePage() {
  const [params] = useSearchParams()
  const code = params.get('code')?.trim()
  return code ? <Request code={code} /> : <CodeForm />
}
