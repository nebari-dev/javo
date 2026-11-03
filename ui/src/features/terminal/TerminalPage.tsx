import { useQueryClient } from '@tanstack/react-query'
import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import { createWorkspace, startWorkspace } from '../../api'
import { Card } from '../../components/Card'
import { problemText } from '../../lib/http'
import { useWorkspaces } from '../../lib/queries'

/** The template a person's own terminal is made from. */
const TERMINAL = 'terminal'

/**
 * Opens the person's terminal: the first app made from the Terminal template,
 * started if it is stopped, or a new one in the home folder the first time.
 * It then shows that app's page, which follows the start and has Open.
 */
export function TerminalPage() {
  const apps = useWorkspaces()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [error, setError] = useState<string>()
  const done = useRef(false)

  useEffect(() => {
    if (!apps.data || done.current) return
    done.current = true
    const mine = apps.data.find((w) => w.template === TERMINAL && !w.previewOf)
    void (async () => {
      try {
        let name = mine?.name
        if (!name) {
          name = (await createWorkspace({ body: { displayName: 'Terminal', template: TERMINAL }, throwOnError: true })).data.name
        } else if (mine?.phase === 'Stopped') {
          await startWorkspace({ path: { name }, throwOnError: true })
        }
        void qc.invalidateQueries({ queryKey: ['workspaces'], exact: true })
        navigate(`/apps/${encodeURIComponent(name)}`, { replace: true })
      } catch (err) {
        setError(problemText(err))
      }
    })()
  }, [apps.data, navigate, qc])

  const failed = error ?? (apps.isError ? problemText(apps.error) : undefined)
  return (
    <Card className="pad">
      {failed ? (
        <p className="errmsg" role="alert">
          {failed}
        </p>
      ) : (
        <p className="muted" role="status">
          Opening your terminal
        </p>
      )}
    </Card>
  )
}
