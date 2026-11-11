import { useMutation, useQuery } from '@tanstack/react-query'
import { Bot, Check, CircleAlert, CircleCheck, X } from 'lucide-react'
import { useId, useState, type FormEvent } from 'react'
import { useNavigate, useSearchParams } from 'react-router'
import { approveDeviceLogin, denyDeviceLogin, getDeviceLogin } from '../../api'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { ago } from '../../lib/ago'
import { ApiError, problemText } from '../../lib/http'

/** What the agent's token may do, by area. */
const GRANTS: [string, string][] = [
  ['Apps and jobs', 'Create, change, start, stop and delete them'],
  ['Files', 'Write in your home folder'],
  ['Secrets', 'Store and change them'],
  ['Sharing', 'Share your apps, including publicly'],
  ['API access', 'Give an app its own, which lasts until you turn it off'],
]

function hours(h: number): string {
  if (h < 1) return 'less than an hour'
  return h === 1 ? '1 hour' : `${h} hours`
}

/** The words for a failed lookup, approve or deny, by what the server said. */
function requestProblem(err: unknown): string {
  if (err instanceof ApiError && err.status === 409) return 'This request was already answered.'
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
    const c = code.replace(/[^a-z0-9]/gi, '').toUpperCase()
    if (c) navigate(`/device?code=${encodeURIComponent(c.length > 4 ? `${c.slice(0, 4)}-${c.slice(4)}` : c)}`)
  }
  return (
    <>
      <h1>Approve an agent login</h1>
      <Card className="pad section device-form">
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
    </>
  )
}

/** The request as an approval card, like the inbox's: the agent on the left; the question, what it grants and the buttons on the right. */
function Request({ code }: { code: string }) {
  const titleId = useId()
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
  const problem = q.isError ? requestProblem(q.error) : decide.isError ? requestProblem(decide.error) : ''
  const earlier = q.data && q.data.state !== 'pending' ? 'This request was already answered.' : ''
  const problemLine = problem && (
    <p className="rq-done bad" role="alert">
      <CircleAlert size={17} aria-hidden="true" />
      <span>{problem}</span>
    </p>
  )
  return (
    <>
      <h1>Approve an agent login</h1>
      <div className="section rq-stack">
        {q.isPending ? (
          <div className="muted">Loading</div>
        ) : !q.data ? (
          <Card className="pad">{problemLine}</Card>
        ) : (
          <Card className={result || earlier ? 'rq decided' : 'rq'} role="article" aria-labelledby={titleId}>
            <div className="who">
              <span className="agent">
                <Bot size={20} aria-hidden="true" />
              </span>
              <b>Agent on your computer</b>
              <span>{`Requested ${ago(q.data.createdAt)}`}</span>
            </div>
            <div className="what">
              <div className="what-top">
                <h2 id={titleId}>Let an agent act as you?</h2>
                <code className="devcode" title="Check it matches the code your agent shows">
                  {q.data.userCode}
                </code>
              </div>
              <p className="say">
                An agent asked to act as you through the API for {hours(q.data.tokenHours)}. Approve only if you started this yourself just now.
              </p>
              <ul className="grants" aria-label="What the agent can do">
                {GRANTS.map(([area, can]) => (
                  <li key={area}>
                    <b>{area}</b>
                    <span>{can}</span>
                  </li>
                ))}
              </ul>
              <p className="cant">It can't use admin powers, make or revoke tokens, or approve requests.</p>
              <p className="devwarn warn">
                <CircleAlert size={17} aria-hidden="true" />
                <span>This gives the agent the same power you have as a member.</span>
              </p>
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
              {problemLine}
            </div>
          </Card>
        )}
      </div>
    </>
  )
}

/** The page an agent's login link opens: what the agent would get, and Approve or Deny. */
export function DevicePage() {
  const [params] = useSearchParams()
  const code = params.get('code')?.trim()
  return code ? <Request code={code} /> : <CodeForm />
}
