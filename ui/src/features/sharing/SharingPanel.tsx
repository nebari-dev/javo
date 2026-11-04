import { Users, X } from 'lucide-react'
import type { SharingInputBody, SharingView, Workspace } from '../../api'

type Access = SharingView['access']
import { fieldErrors, problemText } from '../../lib/http'
import { useSetSharing, useSharing } from '../../lib/queries'
import { Banner } from '../../components/Banner'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { CopyLink } from '../../components/CopyLink'
import { PeoplePicker, type Picked } from './PeoplePicker'
import { PartIcon } from '../../components/Section'
import { Segmented } from '../../components/Segmented'

const RANK: Access[] = ['Private', 'Restricted', 'SignedIn', 'Public']
const rank = (a: Access) => RANK.indexOf(a)

/** The three choices of General access. Private is "chosen people" with nobody chosen. */
type General = 'Restricted' | 'SignedIn' | 'Public'

/** Chosen people with nobody chosen is Private; the API treats that as the same level. */
function restricted(users: string[], groups: string[]): Access {
  return users.length + groups.length > 0 ? 'Restricted' : 'Private'
}

function SharingForm({ name, view }: { name: string; view: SharingView }) {
  const save = useSetSharing(name)
  const users = view.users ?? []
  const groups = view.groups ?? []
  const userIds = users.map((u) => u.id)
  const groupValues = groups.map((g) => g.value)
  const canShareWithPeople = rank(view.maxAccess) >= rank('Restricted')

  const send = (body: SharingInputBody) => save.mutate(body)
  const add = (p: Picked) => {
    const next = { users: [...userIds], groups: [...groupValues] }
    if (p.kind === 'user') next.users.push(p.id)
    else next.groups.push(p.value)
    send({ ...next, access: view.access === 'Private' ? 'Restricted' : view.access })
  }
  const removeUser = (id: string) => {
    const next = userIds.filter((u) => u !== id)
    send({ users: next, groups: groupValues, access: view.access === 'Restricted' ? restricted(next, groupValues) : view.access })
  }
  const removeGroup = (value: string) => {
    const next = groupValues.filter((g) => g !== value)
    send({ users: userIds, groups: next, access: view.access === 'Restricted' ? restricted(userIds, next) : view.access })
  }
  const setGeneral = (g: General) =>
    send({ users: userIds, groups: groupValues, access: g === 'Restricted' ? restricted(userIds, groupValues) : g })

  const current: General | '' = !canShareWithPeople ? '' : view.access === 'Private' ? 'Restricted' : (view.access as General)
  // While a save is in flight every control is locked, so the next change is built from the saved list.
  const busy = save.isPending
  // A refused change carries its reasons per field (body.access, body.users[i]); show those, not the form's title.
  const errs = save.isError ? fieldErrors(save.error) : undefined
  const { access: accessErr, ...rest } = errs?.fields ?? {}
  const otherErrs = [...Object.values(rest), ...(errs?.other ?? [])]

  return (
    <div className="two">
      <Card className="pad">
        <h2 className="label">
          <PartIcon part="sharing" size={15} />
          Who can open this app
        </h2>
        {view.actsAsOwner && (
          // An app that can write as its owner is a way to act as them for anyone it is shared with.
          <Banner tone={view.apiAccess === 'ReadWrite' && view.access !== 'Private' ? 'warn' : 'info'} title={view.actsAsOwner} />
        )}
        {view.exposes && (
          // What the people it is shared with get through this kind of app, from the API.
          <Banner tone="warn" title={view.exposes} />
        )}
        {canShareWithPeople && (
          <>
            <p className="muted">People you add can open the app. They never get your sign-in.</p>
            <PeoplePicker taken={[...userIds, ...groupValues]} onPick={add} disabled={busy} />
            <ul className="people">
              {users.map((u) => (
                <li key={u.id}>
                  <span className="initials" aria-hidden="true">
                    {u.displayName.charAt(0).toUpperCase()}
                  </span>
                  <span className="grow">
                    <b>{u.displayName}</b>
                    <span className="faint small block">{u.email}</span>
                    {u.note && <span className="small block err-text">{u.note}</span>}
                  </span>
                  <span className="faint small">Can open</span>
                  <Button variant="ghost" size="sm" icon={X} aria-label={`Remove ${u.displayName}`} disabled={busy} onClick={() => removeUser(u.id)} />
                </li>
              ))}
              {groups.map((g) => (
                <li key={g.value}>
                  <span className="initials" aria-hidden="true">
                    <Users size={16} />
                  </span>
                  <span className="grow">
                    <b>{g.label}</b>
                    <span className="faint small block mono">{g.value}</span>
                  </span>
                  <span className="faint small">Can open</span>
                  <Button variant="ghost" size="sm" icon={X} aria-label={`Remove ${g.label}`} disabled={busy} onClick={() => removeGroup(g.value)} />
                </li>
              ))}
            </ul>
          </>
        )}
        {otherErrs.map((m) => (
          <Banner key={m} tone="err" title={m} />
        ))}
        <div className="field" role="group" aria-label="General access">
          <div className="field-label">General access</div>
          <Segmented
            label="General access"
            value={current}
            onChange={setGeneral}
            options={(view.levels ?? []).map((l) => ({ value: l.value as General, label: l.label, disabled: busy || rank(l.value) > rank(view.maxAccess) }))}
          />
          {view.tooOpen && <span className="hint">{view.tooOpen}</span>}
          {accessErr && <Banner tone="err" title={accessErr} />}
        </div>
        <CopyLink link={view.link} />
      </Card>
    </div>
  )
}

/** The Terminal, JupyterLab and coding agents run code as the owner, so they can only ever be private. */
function PrivateOnly() {
  return (
    <Card className="pad">
      <h2 className="label">
        <PartIcon part="private" size={15} />
        Only you can open this app
      </h2>
      <p className="muted tight">
        Terminals, JupyterLab and coding agents cannot be shared: anyone who opens it can run code as you, with your files. To share
        your work, run it as an app that shows results, such as Streamlit, Panel or Voila.
      </p>
    </Card>
  )
}

export function SharingPanel({ ws }: { ws: Pick<Workspace, 'name'> }) {
  const q = useSharing(ws.name)
  if (q.isError) return <Banner tone="err" title={problemText(q.error)} />
  if (!q.data) return <div className="muted">Loading</div>
  if (q.data.maxAccess === 'Private') return <PrivateOnly />
  return <SharingForm name={ws.name} view={q.data} />
}
