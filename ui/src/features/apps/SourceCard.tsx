import { GitBranch, RefreshCw } from 'lucide-react'
import { PartIcon } from '../../components/Section'
import { useState } from 'react'
import type { Workspace } from '../../api'
import { fieldErrors, problemText } from '../../lib/http'
import { useRedeploy, useSecrets, useSetSourceToken } from '../../lib/queries'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { CodeBlock } from '../../components/CodeBlock'
import { More } from '../../components/More'
import { TokenSecretField } from './TokenSecretField'

const SHORT_COMMIT = 7

/** The access token of a private repository: the secret it comes from, changed behind a disclosure. */
function TokenRow({ ws }: { ws: Workspace }) {
  const set = useSetSourceToken(ws.name)
  const now = ws.source?.tokenSecret ?? ''
  const [picked, setPicked] = useState(now)
  const mine = useSecrets().data ?? []
  const err = set.isError ? (fieldErrors(set.error).fields.secret ?? problemText(set.error)) : undefined
  return (
    <>
      <dt>Private repo token</dt>
      <dd className={now ? 'mono' : undefined}>{now ? `From ${now}` : ws.source?.hasToken ? 'Set' : <span className="muted">Not set</span>}</dd>
      <dd className="size-change">
        <More
          closed={now || ws.source?.hasToken ? 'Change or remove the access token' : 'Add a token for a private repository'}
          open="Hide the access token options"
        >
          <div className="stack">
            <TokenSecretField
              label="Token from"
              value={picked}
              onChange={setPicked}
              available={mine.map((s) => s.name)}
              error={err}
              hint="The secret that holds the repository's token. The app restarts when you save."
            />
            <div className="row">
              <Button size="sm" variant="primary" disabled={!picked || picked === now || set.isPending} onClick={() => set.mutate(picked)}>
                Save token
              </Button>
              {(now || ws.source?.hasToken) && (
                <Button size="sm" disabled={set.isPending} onClick={() => set.mutate(null, { onSuccess: () => setPicked('') })}>
                  Remove token
                </Button>
              )}
            </div>
          </div>
        </More>
      </dd>
    </>
  )
}

/**
 * What an app runs from and Redeploy. An app from a Git repository shows the
 * repository, branch, deployed commit and token; a static app from a home
 * file shows Redeploy alone; any other app shows nothing. A preview is
 * managed by its app, so it has Redeploy but no token controls.
 */
export function SourceCard({ ws }: { ws: Workspace }) {
  const redeploy = useRedeploy(ws.name)
  const src = ws.source
  if (!ws.redeployable) return null
  const settling = ws.phase === 'Starting' || ws.phase === 'Pending' || ws.phase === 'Stopping'
  return (
    <Card className="pad">
      <div className="label">
        <GitBranch size={15} aria-hidden="true" />
        {src ? 'Source' : 'Deployed from'}
      </div>
      <dl className="kv">
        {src ? (
          <>
            <dt className="wide">
              <PartIcon part="code" />
              Repository
            </dt>
            <dd className="wide">
              <a href={src.url} target="_blank" rel="noreferrer">
                {src.url.replace(/^https:\/\//, '')}
              </a>
            </dd>
            <dt>Branch or tag</dt>
            <dd>{src.ref ? <span className="mono">{src.ref}</span> : <span className="muted">Default branch</span>}</dd>
            {src.path && (
              <>
                <dt>Folder</dt>
                <dd className="mono">{src.path}</dd>
              </>
            )}
            <dt>Commit</dt>
            <dd>
              {!src.commit ? (
                <span className="muted">Not cloned yet</span>
              ) : src.webURL ? (
                <a className="mono" href={src.webURL} target="_blank" rel="noreferrer">
                  {src.commit.slice(0, SHORT_COMMIT)}
                </a>
              ) : (
                <span className="mono">{src.commit.slice(0, SHORT_COMMIT)}</span>
              )}
            </dd>
            {!ws.previewOf && <TokenRow ws={ws} />}
          </>
        ) : (
          <>
            <dt className="wide">
              <PartIcon part="code" />
              Home file
            </dt>
            <dd className="wide">
              <CodeBlock text={ws.file} label="Copy path" />
            </dd>
          </>
        )}
      </dl>
      <div className="redeploy">
        <Button icon={RefreshCw} disabled={settling || redeploy.isPending} onClick={() => redeploy.mutate()}>
          Redeploy
        </Button>
        <span className="muted small">
          {src ? `Pulls the newest commit${src.ref ? ` of ${src.ref}` : ''} and restarts the app.` : 'Copies the file again from your home folder and restarts the app.'}
        </span>
      </div>
      {redeploy.isError && <span className="errmsg">{problemText(redeploy.error)}</span>}
    </Card>
  )
}
