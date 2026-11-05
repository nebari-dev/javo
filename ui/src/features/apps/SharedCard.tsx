import type { SharedApp } from '../../api'
import { useTemplateName } from '../../lib/queries'
import { appLink, pillStatus } from './status'
import { CardMark } from './CardThumb'
import { StatusIcon } from '../../components/Pill'
import { useIsAdmin } from '../settings/admin/queries'
import { OthersAppActions } from './OthersAppActions'
import { PromptLine } from './AgentPrompt'

/**
 * An app another person shared; it opens the app itself, since viewers have
 * no management page. A platform admin also gets Start or Stop and Delete,
 * so the name's link covers the card instead of the card being the link.
 */
export function SharedCard({ app }: { app: SharedApp }) {
  const template = useTemplateName(app.template)
  const admin = useIsAdmin()
  const href = app.phase === 'Running' ? appLink(app.url) : undefined
  const title = <h3>{app.displayName}</h3>
  const body = (
    <>
      <div className="acard-head">
        <CardMark kind={app.kind} thumbnail={app.thumbnail} />
        <div className="grow">
          {admin && href ? (
            <a href={href} className="acard-link" target="_blank" rel="noopener">
              {title}
            </a>
          ) : (
            title
          )}
          <PromptLine prompt={app.agentPrompt} className="acard-sub" />
          <div className="acard-sub">
            {!app.agentPrompt && <>{template} · </>}
            <span>from {app.owner.displayName}</span>
          </div>
          <div className="acard-sub">{sharedHow(app.via)}</div>
        </div>
        <StatusIcon status={pillStatus(app.phase)} />
      </div>
    </>
  )
  if (admin)
    return (
      <div className="card acard">
        {body}
        <div className="acard-foot">
          <div className="acard-actions">
            <OthersAppActions app={app} />
          </div>
        </div>
      </div>
    )
  return href ? (
    <a href={href} className="card acard" target="_blank" rel="noopener">
      {body}
    </a>
  ) : (
    <div className="card acard">{body}</div>
  )
}

/** How an app reaches the viewer, from the listing's `via`. */
export function sharedHow(via: string): string {
  if (via.startsWith('group:')) return `Shared with your group ${via.slice('group:'.length).split('/').pop()}`
  if (via === 'signed-in') return 'Shared with everyone signed in'
  if (via === 'public') return 'Anyone with the link'
  return 'Shared with you'
}
