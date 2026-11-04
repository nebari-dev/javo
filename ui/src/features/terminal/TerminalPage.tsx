import { useQueryClient } from '@tanstack/react-query'
import { Play } from 'lucide-react'
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router'
import { createWorkspace, startWorkspace } from '../../api'
import { Card } from '../../components/Card'
import { Confirm } from '../../components/Confirm'
import { problemText } from '../../lib/http'
import { useWorkspaces } from '../../lib/queries'

/** The template a person's own terminal is made from. */
const TERMINAL = 'terminal'

/**
 * Opens the person's terminal: the first app made from the Terminal template.
 * A running one opens at once. Otherwise the page asks first, then starts the
 * stopped one or makes a new one in the home folder, and shows that app's
 * page, which follows the start and has Open. Cancel shows a stopped
 * terminal's page without starting it, or goes back when there is none.
 */
export function TerminalPage() {
  const apps = useWorkspaces()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const mine = apps.data?.find((w) => w.template === TERMINAL && !w.previewOf)
  const running = !!mine && mine.phase !== 'Stopped'

  useEffect(() => {
    if (mine && running) navigate(`/apps/${encodeURIComponent(mine.name)}`, { replace: true })
  }, [mine, running, navigate])

  const start = async () => {
    setBusy(true)
    try {
      let name = mine?.name
      if (!name) {
        name = (await createWorkspace({ body: { displayName: 'Terminal', template: TERMINAL }, throwOnError: true })).data.name
      } else {
        await startWorkspace({ path: { name }, throwOnError: true })
      }
      void qc.invalidateQueries({ queryKey: ['workspaces'], exact: true })
      navigate(`/apps/${encodeURIComponent(name)}`, { replace: true })
    } catch (err) {
      setError(problemText(err))
      setBusy(false)
    }
  }
  const cancel = () => (mine ? navigate(`/apps/${encodeURIComponent(mine.name)}`, { replace: true }) : navigate(-1))

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
      {apps.data && !running && !failed && (
        <Confirm
          title={mine ? 'Start your terminal?' : 'Start a terminal?'}
          body={
            mine
              ? 'It starts a shell in your home folder, and uses CPU and memory until it stops.'
              : 'A terminal app is made with a shell in your home folder. It uses CPU and memory until it stops.'
          }
          confirm="Start"
          icon={Play}
          busy={busy}
          onConfirm={() => void start()}
          onCancel={cancel}
        />
      )}
    </Card>
  )
}
