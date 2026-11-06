import { CircleAlert, CircleCheck, EyeOff, FolderOpen, Power, RotateCw, Trash2, Undo2, type LucideIcon } from 'lucide-react'
import type { ReactNode } from 'react'
import type { Plan } from '../../api'
import { ago } from '../../lib/ago'

/** How a consequence weighs on the person: safe or reversible, interrupts an app, cannot be undone. */
export type Tone = 'ok' | 'warn' | 'err'

/** One consequence: a chip of a few words, and the sentence the single-request page adds under it. */
export interface Consequence {
  tone: Tone
  icon: LucideIcon
  text: string
  detail: string
}

/** A request in words, all built from the plan's own data. */
export interface Request {
  /** The yes or no question, such as "Restart Report builder?". */
  question: ReactNode
  /** The same question, plain text, for accessible names. */
  questionText: string
  /** The question on the app's own page, where the app is "this app". */
  banner: ReactNode
  bannerText: string
  /** One line on what happens. */
  say: string
  chips: Consequence[]
  /** The main button names the action; danger for one that cannot be undone. */
  primary: string
  danger: boolean
  /** Asked before a change that cannot be undone. */
  confirm?: { title: string; body: string; yes: string; no: string }
  /** What happened, in the card and the toast. */
  applied: string
  toast: string
  /** "give access", for "Couldn't give access: ...". */
  verb: string
  /** "You gave access.", for a request decided earlier. */
  past: string
  /** The rows under Details. */
  details: { label: string; value: ReactNode }[]
}

/** The agent that asked, as the person knows it. */
export interface Agent {
  name: string
  /** The template kind whose logo to show; empty for a generic agent icon. */
  kind: string
  /** "in Sales agent", "with your API token laptop"; empty for the person's own session. */
  where: string
}

// Agents whose names and logos Javo knows, by template kind and by the name
// they give themselves over MCP.
const AGENTS: Record<string, string> = { pi: 'Pi', 'claude-code': 'Claude Code' }

const SOURCES: Record<string, string> = { rules: 'Failure check', 'ai-diagnosis': 'AI explanation', palette: 'Search' }

const key = (s: string) => s.trim().toLowerCase().replace(/\s+/g, '-')

/** Who asked for plan: the agent's name and logo, and where it asked from. */
export function agentOf(plan: Plan): Agent {
  const by = plan.requestedBy ?? {}
  const self = plan.source.startsWith('mcp:') ? plan.source.slice(4) : ''
  const kind = by.appKind || (AGENTS[key(self)] ? key(self) : '')
  const name = AGENTS[kind] ?? (self || SOURCES[plan.source] || 'An agent')
  let where = ''
  if (by.app) where = `in ${by.appName || by.app}`
  else if (by.token) where = `with your API token ${by.token}`
  else if (by.job) where = 'from one of your jobs'
  return { name, kind, where }
}

/** "in Sales agent, 2 minutes ago". */
export function askedLine(plan: Plan, now = Date.now()): string {
  return [agentOf(plan).where, ago(plan.createdAt, now)].filter(Boolean).join(', ')
}

/** The credential the gateway saw, in words, for Details. */
function credential(plan: Plan): string {
  const by = plan.requestedBy ?? {}
  if (by.app) return `The token of your app ${by.appName || by.app}`
  if (by.token) return `Your API token ${by.token}`
  if (by.job) return 'The token of one of your jobs'
  return 'You, signed in here'
}

/** Minutes until plan expires, rounded up; 0 once it has. */
export function minutesLeft(plan: Plan, now = Date.now()): number {
  return Math.max(0, Math.ceil((Date.parse(plan.expiresAt) - now) / 60_000))
}

const cap = (s: string) => s.charAt(0).toUpperCase() + s.slice(1)

const restarts = (app: string): Consequence => ({ tone: 'warn', icon: RotateCw, text: `${app} restarts`, detail: 'It is offline for a moment while the change is made.' })
const kept: Consequence = { tone: 'ok', icon: CircleCheck, text: 'Files and settings kept', detail: 'Nothing in its home folder or settings changes.' }

/** plan as a question the person answers, with what each answer does. */
export function describe(plan: Plan): Request {
  const p = plan.planned
  const app = p.targetName || p.target
  const agent = agentOf(plan).name
  const secret = p.params?.secret ?? ''
  const env = p.params?.env || secret
  const base = { danger: false, details: [] as Request['details'] }
  const appRow = { label: 'App', value: app }
  const tail: Request['details'] = [
    { label: 'Asked with', value: credential(plan) },
    ...(plan.source.startsWith('mcp:') ? [{ label: 'Agent calls itself', value: plan.source.slice(4) }] : []),
    { label: 'Request', value: <code>{plan.id}</code> },
    { label: 'Expires', value: new Date(plan.expiresAt).toLocaleString() },
  ]
  const r = ((): Omit<Request, 'details'> & { details: Request['details'] } => {
    switch (p.action) {
      case 'attach-secret':
        return {
          ...base,
          question: <>Let {app} use your secret <code>{secret}</code>?</>,
          questionText: `Let ${app} use your secret ${secret}?`,
          banner: <>{agent} asks: let this app use your secret <code>{secret}</code>?</>,
          bannerText: `${agent} asks: let this app use your secret ${secret}?`,
          say: `${app} will be able to use the secret. ${agent} will not see it.`,
          chips: [
            { ...restarts(app), detail: 'It is offline for a moment while the secret is added.' },
            { tone: 'ok', icon: EyeOff, text: `Hidden from ${agent}`, detail: `Only ${app} can read the secret. ${agent} never sees it.` },
            { tone: 'ok', icon: Undo2, text: 'You can remove it later', detail: `From ${app}'s settings, at any time.` },
          ],
          primary: 'Give access',
          applied: `Access given. ${app} is restarting with the secret.`,
          toast: `Access given. ${app} is restarting with the secret.`,
          verb: 'give access',
          past: 'You gave access.',
          details: [{ label: 'Secret', value: <code>{secret}</code> }, { label: 'Name inside the app', value: <code>{env}</code> }, appRow],
        }
      case 'detach-secret': {
        const from = p.changes?.[0]?.from.replace(/^secret /, '') || env
        return {
          ...base,
          question: <>Take your secret <code>{from}</code> away from {app}?</>,
          questionText: `Take your secret ${from} away from ${app}?`,
          banner: <>{agent} asks: take your secret <code>{from}</code> away from this app?</>,
          bannerText: `${agent} asks: take your secret ${from} away from this app?`,
          say: `${app} will no longer be able to use the secret. The secret itself is kept.`,
          chips: [
            { ...restarts(app), detail: 'It is offline for a moment while the secret is removed.' },
            { tone: 'ok', icon: CircleCheck, text: 'Your secret is kept', detail: 'Only this app loses it. Your other apps keep theirs.' },
          ],
          primary: 'Remove access',
          applied: `Access removed. ${app} is restarting without the secret.`,
          toast: `Access removed. ${app} is restarting without the secret.`,
          verb: 'remove access',
          past: 'You removed access.',
          details: [{ label: 'Secret', value: <code>{from}</code> }, { label: 'Name inside the app', value: <code>{env}</code> }, appRow],
        }
      }
      case 'restart':
        return {
          ...base,
          question: `Restart ${app}?`,
          questionText: `Restart ${app}?`,
          banner: `${agent} asks: restart this app?`,
          bannerText: `${agent} asks: restart this app?`,
          say: `${app} will stop and start again. Anyone using it will need to reload the page.`,
          chips: [{ tone: 'warn', icon: Power, text: 'Offline for a moment', detail: 'Anyone using it will need to reload the page.' }, kept],
          primary: 'Restart',
          applied: `Restarted. ${app} is starting again.`,
          toast: `${app} is restarting.`,
          verb: `restart ${app}`,
          past: `You restarted ${app}.`,
          details: [appRow],
        }
      case 'stop':
        return {
          ...base,
          question: `Stop ${app}?`,
          questionText: `Stop ${app}?`,
          banner: `${agent} asks: stop this app?`,
          bannerText: `${agent} asks: stop this app?`,
          say: `${app} will stop. Its link does not work until it is started again.`,
          chips: [
            { tone: 'warn', icon: Power, text: `${app} goes offline`, detail: 'Its link does not work while it is stopped.' },
            kept,
            { tone: 'ok', icon: Undo2, text: 'You can start it again', detail: `From ${app}'s page, at any time.` },
          ],
          primary: 'Stop',
          applied: `Stopped. ${app} is shutting down.`,
          toast: `${app} is stopping.`,
          verb: `stop ${app}`,
          past: `You stopped ${app}.`,
          details: [appRow],
        }
      case 'start':
        return {
          ...base,
          question: `Start ${app}?`,
          questionText: `Start ${app}?`,
          banner: `${agent} asks: start this app?`,
          bannerText: `${agent} asks: start this app?`,
          say: `${app} will start. Its link works again once it is running.`,
          chips: [kept, { tone: 'ok', icon: Undo2, text: 'You can stop it again', detail: `From ${app}'s page, at any time.` }],
          primary: 'Start',
          applied: `Started. ${app} is starting.`,
          toast: `${app} is starting.`,
          verb: `start ${app}`,
          past: `You started ${app}.`,
          details: [appRow],
        }
      case 'resize': {
        const size = p.changes?.[0]?.to ?? ''
        return {
          ...base,
          question: `Make ${app} ${cap(size)}?`,
          questionText: `Make ${app} ${cap(size)}?`,
          banner: `${agent} asks: make this app ${cap(size)}?`,
          bannerText: `${agent} asks: make this app ${cap(size)}?`,
          say: `${app} will restart at the new size. Anyone using it will need to reload the page.`,
          chips: [restarts(app), kept, { tone: 'ok', icon: Undo2, text: 'You can change it back', detail: `From ${app}'s page, at any time.` }],
          primary: 'Resize',
          applied: `Resized. ${app} is restarting at the new size.`,
          toast: `${app} is restarting at the new size.`,
          verb: `resize ${app}`,
          past: `You resized ${app}.`,
          details: [appRow, { label: 'Size', value: `${p.changes?.[0]?.from || 'default'} to ${size}` }],
        }
      }
      case 'delete':
        return {
          ...base,
          danger: true,
          question: `Delete ${app}?`,
          questionText: `Delete ${app}?`,
          banner: `${agent} asks: delete this app?`,
          bannerText: `${agent} asks: delete this app?`,
          say: `${app} and its link will be removed. Files in its home folder stay.`,
          chips: [
            { tone: 'err', icon: Trash2, text: 'App and link removed', detail: 'Anyone with the link loses it.' },
            { tone: 'ok', icon: FolderOpen, text: 'Home folder kept', detail: 'Files in its home folder stay where they are.' },
            { tone: 'err', icon: CircleAlert, text: "Can't be undone", detail: 'A deleted app cannot be brought back.' },
          ],
          primary: `Delete ${app}`,
          confirm: {
            title: `Delete ${app} for good?`,
            body: 'The app and its link are removed. Files in its home folder are kept. This cannot be undone.',
            yes: `Delete ${app}`,
            no: `Keep ${app}`,
          },
          applied: 'Deleted. Files in its home folder are kept.',
          toast: `Deleted ${app}. Its home folder is kept.`,
          verb: `delete ${app}`,
          past: `You deleted ${app}.`,
          details: [appRow],
        }
    }
    // An action this page has no words for: the server's own summary.
    return {
      ...base,
      danger: p.destructive,
      question: p.summary,
      questionText: p.summary,
      banner: `${agent} asks: ${p.summary}`,
      bannerText: `${agent} asks: ${p.summary}`,
      say: '',
      chips: p.destructive ? [{ tone: 'err', icon: CircleAlert, text: "Can't be undone", detail: 'This change cannot be reversed.' }] : [],
      primary: 'Approve',
      confirm: p.destructive ? { title: `${p.summary}?`, body: 'This cannot be undone.', yes: 'Approve', no: 'Cancel' } : undefined,
      applied: 'Done.',
      toast: 'Done.',
      verb: 'make the change',
      past: 'You approved it.',
      details: [appRow],
    }
  })()
  return { ...r, details: [...r.details, ...tail] }
}

/** What became of a decided plan, in words: the result of deciding it now, or of an earlier decision. */
export function outcome(plan: Plan, r: Request, earlier = false): string {
  const agent = agentOf(plan).name
  switch (plan.state) {
    case 'applied':
    case 'applying':
      return earlier ? `Already decided. ${r.past}` : r.applied
    case 'rejected':
      return earlier ? 'Already decided. You declined it. Nothing changed.' : 'Declined. Nothing changed.'
    case 'expired':
      return `Expired. Nothing changed. ${agent} can ask again.`
    case 'failed':
      return failed(r, plan.error || 'it did not work')
  }
  return ''
}

/** "Couldn't give access: {reason}. Nothing changed." */
export function failed(r: Request, reason: string): string {
  return `Couldn't ${r.verb}: ${reason.replace(/\.$/, '')}. Nothing changed.`
}
