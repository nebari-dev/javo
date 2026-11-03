import { CheckCheck, Plus, Server, Sparkles } from 'lucide-react'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { useAdminReport, useAiFeature, useAiCommand, useAskAdmin, useDecidePlan } from '../ai/ai'
import type { DraftView, Plan } from '../../api'
import { Button } from '../../components/Button'
import { PalettePanel, usePalette, type PaletteItem, type PaletteSection } from './Palette'
import { PlanReview } from '../ai/Plan'
import { problemText } from '../../lib/http'
import { useMe, useSizes, useTemplates } from '../../lib/queries'

/** Runs fn once, on mount, with the text the panel was opened for. */
function useOnce(fn: () => void) {
  const done = useRef(false)
  useEffect(() => {
    if (!done.current) {
      done.current = true
      fn()
    }
  }, [fn])
}

/**
 * A new app the model drafted from the request, in one line, with a button
 * that opens New app filled in with it. Nothing is created until the person
 * presses Create there.
 */
function DraftProposal({ draft }: { draft: DraftView }) {
  const navigate = useNavigate()
  const close = usePalette()
  const tpl = useTemplates().data?.find((t) => t.name === draft.draft.template)
  const size = useSizes().data?.find((s) => s.default)
  if (draft.message) return <p role="status">{draft.message}</p>
  const d = draft.draft
  const line = [
    `New ${tpl?.displayName ?? d.template} app${d.displayName ? ` '${d.displayName}'` : ''}${d.file ? ` from ${d.file}` : ''}`,
    size?.displayName,
  ]
    .filter(Boolean)
    .join(', ')
  return (
    <div className="row">
      <span>{line}</span>
      <Button
        variant="primary"
        icon={Plus}
        onClick={() => {
          close()
          void navigate('/apps/new', { state: { draft } })
        }}
      >
        Review in New app
      </Button>
    </div>
  )
}

/**
 * The model's answer to a request typed into the palette: its reply and one
 * plan per change, each with its diff. Nothing changes until a plan is
 * approved, one by one or all at once.
 */
function AskPanel({ text }: { text: string }) {
  const ask = useAiCommand()
  const decide = useDecidePlan()
  const [plans, setPlans] = useState<Plan[]>([])
  const { mutate } = ask
  useOnce(() => mutate(text, { onSuccess: (r) => setPlans(r.plans ?? []) }))
  const pending = plans.filter((p) => p.state === 'pending')
  const approveAll = async () => {
    for (const p of pending) {
      const done = await decide.mutateAsync({ id: p.id, approve: true }).catch(() => undefined)
      if (done) setPlans((ps) => ps.map((x) => (x.id === done.id ? done : x)))
    }
  }
  return (
    <PalettePanel>
      <div className="pal-ask" aria-live="polite">
        <div className="label">
          <Sparkles size={15} aria-hidden="true" />
          {text}
        </div>
        {ask.isPending && <p className="muted">Thinking about it</p>}
        {ask.isError && <p role="alert">{problemText(ask.error)}</p>}
        {ask.data?.message && <p role="status">{ask.data.message}</p>}
        {ask.data?.reply && <p>{ask.data.reply}</p>}
        {ask.data?.draft && <DraftProposal draft={ask.data.draft} />}
        {plans.map((p) => (
          <PlanReview key={`${p.id}:${p.state}`} plan={p} applyLabel="Approve" onDone={(d) => setPlans((ps) => ps.map((x) => (x.id === d.id ? d : x)))} />
        ))}
        {plans.length > 1 && pending.length > 0 && (
          <div className="row">
            <Button variant="primary" icon={CheckCheck} disabled={decide.isPending} onClick={() => void approveAll()}>
              Approve all
            </Button>
          </div>
        )}
        {ask.data && <p className="ai-note">Nothing changes until you approve it.</p>}
      </div>
    </PalettePanel>
  )
}

/** A platform admin's question about the cluster: the report rows and an answer that cites them. */
function AdminPanel({ text }: { text: string }) {
  const report = useAdminReport(true)
  const ask = useAskAdmin()
  const { mutate } = ask
  useOnce(() => mutate(text))
  const cited = new Set(ask.data?.refs ?? [])
  return (
    <PalettePanel>
      <div className="pal-ask" aria-live="polite">
        <div className="label">
          <Server size={15} aria-hidden="true" />
          {text}
        </div>
        {ask.isPending && <p className="muted">Reading the report</p>}
        {ask.isError && <p role="alert">{problemText(ask.error)}</p>}
        {ask.data?.message && <p role="status">{ask.data.message}</p>}
        {ask.data?.answer && <p>{ask.data.answer}</p>}
        {report.data && (
          <ul className="evidence-list" aria-label="Report">
            {(report.data.rows ?? []).map((r) => (
              <li key={r.ref} className={cited.has(r.ref) ? 'cited' : undefined}>
                <span className="ev-text">{r.title}</span>
                <span className="ev-ref">
                  {r.value % 1 ? r.value.toFixed(1) : r.value} {r.unit}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </PalettePanel>
  )
}

const ask = (id: string, label: string, icon: ReactNode, panel: ReactNode): PaletteItem => ({
  id,
  label,
  icon,
  actions: panel,
})

/**
 * The palette's AI rows for a query: "Ask AI" for changes to the person's
 * apps, and for platform admins "Ask about this cluster". Undefined when AI
 * (or that feature) is off. The last query is kept so an opened row keeps
 * its text after the field clears.
 */
export function useAskSection(): (query: string) => PaletteSection | undefined {
  const palette = useAiFeature('palette')
  const adminOn = useAiFeature('admin')
  const roles = useMe().data?.roles ?? []
  const admin = adminOn && roles.includes('platform-admin')
  const last = useRef('')
  return (query: string) => {
    if (query) last.current = query
    const text = query || last.current
    if (!text || (!palette && !admin)) return undefined
    const items: PaletteItem[] = []
    if (palette) items.push(ask('ai:ask', `Ask AI: ${text}`, <Sparkles size={16} />, <AskPanel text={text} />))
    if (admin) items.push(ask('ai:admin', `Ask about this cluster: ${text}`, <Server size={16} />, <AdminPanel text={text} />))
    return { heading: 'Ask AI', items }
  }
}
