import { Bot, Check, CircleAlert, CircleCheck, Hourglass, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import type { Plan } from '../../api'
import { Button } from '../../components/Button'
import { Confirm } from '../../components/Confirm'
import { KindLogo } from '../../components/KindLogo'
import { problemText } from '../../lib/http'
import { useDecidePlan } from '../ai/ai'
import { agentOf, describe, failed, minutesLeft, outcome, outcomeText, type Consequence } from './copy'

/** The asking agent's own logo, or a plain agent icon when Javo does not know it. */
export function AgentMark({ plan }: { plan: Plan }) {
  const { kind } = agentOf(plan)
  return <span className="agent">{kind ? <KindLogo kind={kind} /> : <Bot size={20} aria-hidden="true" />}</span>
}

/** The consequences as coloured chips; the words carry the meaning, the colour backs them. */
export function Chips({ items, label = 'What happens' }: { items: Consequence[]; label?: string }) {
  if (items.length === 0) return null
  return (
    <ul className="chips" aria-label={label}>
      {items.map((c) => (
        <li key={c.text} className={`chip ${c.tone}`}>
          <c.icon size={14} aria-hidden="true" />
          {c.text}
        </li>
      ))}
    </ul>
  )
}

/** Minutes left to decide; amber under five. */
export function Timer({ plan }: { plan: Plan }) {
  const n = minutesLeft(plan)
  return (
    <span className={n < 5 ? 'timer soon' : 'timer'}>
      <Hourglass size={13} aria-hidden="true" />
      {n > 0 ? `${n} min left` : 'Expired'}
    </span>
  )
}

/** A toast at the bottom of the page that repeats what a decision did. */
export function useToast() {
  const [text, setText] = useState('')
  const timer = useRef<ReturnType<typeof setTimeout>>(undefined)
  useEffect(() => () => clearTimeout(timer.current), [])
  const show = (t: string) => {
    setText(t)
    clearTimeout(timer.current)
    timer.current = setTimeout(() => setText(''), 4200)
  }
  const node = (
    <div className={text ? 'toast show' : 'toast'} role="status" aria-live="polite">
      {text && <CircleCheck size={17} aria-hidden="true" />}
      <span>{text}</span>
    </div>
  )
  return { show, node }
}

/**
 * Deciding one plan: the main button (asking first when the change cannot
 * be undone), Decline, and what became of it. onDecided hears every
 * answer, so a list can keep the card on screen with its result.
 */
export function useDecision(plan: Plan, { onDecided, toast }: { onDecided?: (p: Plan) => void; toast?: (t: string) => void }) {
  const decide = useDecidePlan()
  const r = describe(plan)
  const [current, setCurrent] = useState(plan)
  const [error, setError] = useState('')
  const [asking, setAsking] = useState(false)
  const run = (approve: boolean) =>
    decide.mutate(
      { id: plan.id, approve },
      {
        onSuccess: (p) => {
          setCurrent(p)
          const text = outcomeText(p, r)
          if (p.state === 'failed') setError(text)
          else toast?.(approve ? r.toast : text)
          onDecided?.(p)
        },
        onError: (e) => {
          setError(failed(r, problemText(e)))
          onDecided?.(current)
        },
        onSettled: () => setAsking(false),
      },
    )
  const decided = current.state !== 'pending' || !!error
  // Decided before this card opened (an approve link followed late): say it was already decided.
  const [earlier] = useState(plan.state !== 'pending')
  return {
    r,
    current,
    decided,
    /** The result line once decided; empty while it waits. */
    result: error || (current.state !== 'pending' ? outcome(current, r, earlier) : ''),
    ok: !error && current.state !== 'failed',
    busy: decide.isPending,
    approve: () => (r.confirm ? setAsking(true) : run(true)),
    decline: () => run(false),
    confirm:
      asking && r.confirm ? (
        <Confirm
          title={r.confirm.title}
          body={r.confirm.body}
          confirm={r.confirm.yes}
          cancel={r.confirm.no}
          icon={Check}
          danger={r.danger}
          busy={decide.isPending}
          onConfirm={() => run(true)}
          onCancel={() => setAsking(false)}
        />
      ) : null,
  }
}

/** The main button, named for the action, and Decline. */
export function Actions({ d }: { d: ReturnType<typeof useDecision> }) {
  return (
    <>
      <Button variant={d.r.danger ? 'danger' : 'primary'} icon={Check} disabled={d.busy} onClick={d.approve}>
        {d.r.primary}
      </Button>
      <Button icon={X} disabled={d.busy} onClick={d.decline}>
        Decline
      </Button>
    </>
  )
}

/** The result of a decision, in the place the question's buttons were. */
export function Result({ d }: { d: ReturnType<typeof useDecision> }) {
  if (!d.result) return null
  return (
    <p className={d.ok ? 'rq-done' : 'rq-done bad'} role={d.ok ? undefined : 'alert'}>
      {d.ok ? <CircleCheck size={17} aria-hidden="true" /> : <CircleAlert size={17} aria-hidden="true" />}
      <span>{d.result}</span>
    </p>
  )
}
