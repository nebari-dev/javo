import { Banner } from '../../../components/Banner'
import { Card } from '../../../components/Card'
import { Switch } from '../../../components/Switch'
import { problemText } from '../../../lib/http'
import { useAgentSettings, useIsAdmin, useSetAgentSettings } from './queries'

/**
 * Settings, Agents (platform admins): whether owners may let their coding
 * agents act without asking. Off, no one can turn it on and every agent that
 * acts without asking asks again at once.
 */
export function AdminAgents() {
  const admin = useIsAdmin()
  const q = useAgentSettings(admin)
  const set = useSetAgentSettings()
  if (!admin) return null
  if (q.isError) return <Banner tone="err" title={problemText(q.error)} />
  if (!q.data) return <div className="muted">Loading</div>
  const s = q.data
  return (
    <section className="section" aria-label="Agents">
      <p className="sub">
        An owner can let their coding agent act as them without asking, for up to 24 hours. Then a web page, file or package that tricks the
        agent can act as its owner.
      </p>
      <div className="section-sm">
        <Card className="pad">
          {s.configAllows ? (
            <div className="field fit tight">
              <Switch label="Owners may let their agents act without asking" checked={s.allowTrusted} onChange={(on) => set.mutate(on)} />
              <span className="hint">
                {s.allowTrusted
                  ? 'Turning this off makes every agent ask again at once.'
                  : 'No one can turn it on. Agents propose changes and their owners approve them.'}
              </span>
              {set.isError && <span className="errmsg">{problemText(set.error)}</span>}
            </div>
          ) : (
            <p className="tight muted small">Off for everyone: agents.allowTrusted is false in the platform config.</p>
          )}
        </Card>
      </div>
    </section>
  )
}
