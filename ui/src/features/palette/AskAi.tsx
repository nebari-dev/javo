import { Bot, CheckCheck, Server, Sparkles, type LucideIcon } from 'lucide-react'
import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { KindLogo } from '../../components/KindLogo'
import { agentLabel, useAdminReport, useAgentStatus, useAiFeature, useAiCommand, useAskAdmin, useBuildWithAgent, useDecidePlan } from '../ai/ai'
import type { DraftView, Plan } from '../../api'
import { Button } from '../../components/Button'
import { PalettePanel, usePalette, type PaletteItem, type PaletteSection } from './Palette'
import { PlanReview } from '../ai/Plan'
import { problemText } from '../../lib/http'
import { useCreateWorkspace, useMe, useSizes, useTemplates } from '../../lib/queries'

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
 * The request as the person typed it, at the top of an AI answer. Past three
 * lines it is cut, with a toggle to read it all, so a paragraph never pushes
 * the answer out of the panel.
 */
function AiPrompt({ icon: Icon, text }: { icon: LucideIcon; text: string }) {
  const box = useRef<HTMLSpanElement>(null)
  const [open, setOpen] = useState(false)
  const [cut, setCut] = useState(false)
  // Measured again whenever its box changes size: the panel is still hidden on the first look, and the window can narrow.
  useLayoutEffect(() => {
    const el = box.current
    if (!el || open) return
    const measure = () => setCut(el.scrollHeight > el.clientHeight + 1)
    measure()
    const watch = new ResizeObserver(measure)
    watch.observe(el)
    return () => watch.disconnect()
  }, [text, open])
  return (
    <div className="ai-prompt">
      <Icon size={16} aria-hidden="true" className="ai-prompt-ico" />
      <div className="ai-prompt-body">
        <span ref={box} className={open ? 'ai-prompt-text' : 'ai-prompt-text clamped'}>
          {text}
        </span>
        {cut && (
          <button type="button" className="more-toggle ai-prompt-more" aria-expanded={open} onClick={() => setOpen(!open)}>
            {open ? 'Show less' : 'Show full prompt'}
          </button>
        )}
      </div>
    </div>
  )
}

/**
 * A new app the model drafted from the request, shown as the app it will be,
 * with one button that creates it and opens its page. When the draft left
 * something unsettled, the button opens New app filled in instead, to finish
 * there; nothing is created until Create is pressed.
 */
function DraftProposal({ draft }: { draft: DraftView }) {
  const navigate = useNavigate()
  const close = usePalette()
  const create = useCreateWorkspace()
  const tpl = useTemplates().data?.find((t) => t.name === draft.draft.template)
  const size = useSizes().data?.find((s) => s.default)
  if (draft.message) return <p role="status">{draft.message}</p>
  const d = draft.draft
  const facts = [tpl?.displayName ?? d.template, d.file && `runs ${d.file}`, size?.displayName].filter(Boolean).join(' · ')
  // What stands in the way of creating it here: the draft's own problems, or the API's refusal.
  const left = create.isError ? [problemText(create.error)] : (draft.errors ?? []).map((e) => e.message)
  return (
    <>
      <div className="ai-draft" role="group" aria-label={d.displayName}>
        <KindLogo kind={d.template} />
        <span className="grow">
          <b className="block">{d.displayName}</b>
          <span className="muted small block">{facts}</span>
        </span>
      </div>
      <div className="ai-draft-foot">
        {left.length ? (
          <span className="ai-note" role="alert">
            {left.map((m) => (
              <span key={m}>{m} </span>
            ))}
            Nothing is created until you press Create.
          </span>
        ) : (
          <span className="ai-note">It starts right away. You can stop or delete it at any time.</span>
        )}
        {left.length ? (
          <Button
            variant="primary"
            icon={Sparkles}
            onClick={() => {
              close()
              void navigate('/apps/new', { state: { draft } })
            }}
          >
            Finish setting up
          </Button>
        ) : (
        <Button
          variant="primary"
          icon={Sparkles}
          disabled={create.isPending}
          onClick={() =>
            create.mutate(
              { displayName: d.displayName, template: d.template, ...(d.file ? { file: d.file } : {}) },
              {
                onSuccess: (ws) => {
                  close()
                  void navigate(`/apps/${encodeURIComponent(ws.name)}`)
                },
              },
            )
          }
        >
          Create app
        </Button>
        )}
      </div>
    </>
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
    <PalettePanel title="Ask AI">
      <div className="pal-ask" aria-live="polite">
        <AiPrompt icon={Sparkles} text={text} />
        {ask.isPending && <p className="muted">Thinking about it</p>}
        {ask.isError && <p role="alert">{problemText(ask.error)}</p>}
        {ask.data?.message && <p role="status">{ask.data.message}</p>}
        {ask.data?.reply && !ask.data.draft && <p>{ask.data.reply}</p>}
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
        {plans.length > 0 && <p className="ai-note">Nothing changes until you approve it.</p>}
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
    <PalettePanel title="Ask about this cluster">
      <div className="pal-ask" aria-live="polite">
        <AiPrompt icon={Server} text={text} />
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

/** Starts a coding-agent app on the request, then opens its page, where Open goes to the agent already at work. */
function BuildPanel({ text, agent, label }: { text: string; agent: string; label: string }) {
  const build = useBuildWithAgent()
  const navigate = useNavigate()
  const close = usePalette()
  const { mutate } = build
  useOnce(() =>
    mutate(text, {
      onSuccess: (ws) => {
        close()
        void navigate(`/apps/${encodeURIComponent(ws.name)}`, { state: { building: true } })
      },
    }),
  )
  return (
    <PalettePanel title={`Build with ${label}`}>
      <div className="pal-ask" aria-live="polite">
        <AiPrompt icon={Bot} text={text} />
        {build.isPending && <p className="muted">Starting {agent}</p>}
        {build.isError && <p role="alert">{problemText(build.error)}</p>}
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
 * apps, and for platform admins "Ask about this cluster". The section is
 * undefined when AI (or that feature) is off, and on says whether any row
 * can come. The last query is kept so an opened row keeps its text after the
 * field clears.
 */
export function useAskSection(): { section: (query: string) => PaletteSection | undefined; on: boolean } {
  const palette = useAiFeature('palette')
  const adminOn = useAiFeature('admin')
  const roles = useMe().data?.roles ?? []
  const admin = adminOn && roles.includes('platform-admin')
  const agent = useAgentStatus().data
  const builds = !!agent?.available && agent.mode !== 'login'
  const last = useRef('')
  const on = palette || admin || builds
  const section = (query: string) => {
    if (query) last.current = query
    const text = query || last.current
    if (!text || !on) return undefined
    const items: PaletteItem[] = []
    if (builds) {
      const name = agent.template
      items.push(ask('ai:build', `Build with ${agentLabel(name)}: ${text}`, <KindLogo kind={agent.template} />, <BuildPanel text={text} agent={agent.templateName || name} label={agentLabel(name)} />))
    }
    if (palette) items.push(ask('ai:ask', `Ask AI: ${text}`, <Sparkles size={16} />, <AskPanel text={text} />))
    if (admin) items.push(ask('ai:admin', `Ask about this cluster: ${text}`, <Server size={16} />, <AdminPanel text={text} />))
    return { heading: 'Ask AI', items }
  }
  return { section, on }
}
