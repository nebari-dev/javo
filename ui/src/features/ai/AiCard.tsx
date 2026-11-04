import { Sparkles } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { useAiFeedback, useAiStatus, useExplain } from './ai'
import type { Evidence, ExplainView } from '../../api'
import { problemText } from '../../lib/http'
import { Button } from '../../components/Button'
import { Card } from '../../components/Card'
import { More } from '../../components/More'
import { Proposals } from './Proposals'
import { PartIcon } from '../../components/Section'

const evidenceId = (ref: string) => `ev-${ref.replace(/[^A-Za-z0-9_-]/g, '-')}`

/** Evidence lines in code type; each line has an id from its ref, so a citation can point at it. */
function EvidenceList({ items }: { items: Evidence[] }) {
  return (
    <ul className="evidence-list" aria-label="Evidence">
      {items.map((e) => (
        <li key={e.ref} id={evidenceId(e.ref)} tabIndex={-1}>
          <span className="ev-ref">{e.ref}</span>
          <span className="ev-text">{e.excerpt}</span>
        </li>
      ))}
    </ul>
  )
}

/** Counts down from seconds and calls done once at zero. */
function useCountdown(seconds: number, done: () => void) {
  const [left, setLeft] = useState(seconds)
  const fired = useRef(false)
  useEffect(() => {
    setLeft(seconds)
    fired.current = false
    if (seconds <= 0) return
    const t = setInterval(() => setLeft((n) => n - 1), 1000)
    return () => clearInterval(t)
  }, [seconds])
  useEffect(() => {
    if (seconds > 0 && left <= 0 && !fired.current) {
      fired.current = true
      done()
    }
  }, [left, seconds, done])
  return left
}

function Answer({ res, bundle, name }: { res: ExplainView; bundle: Evidence[]; name: string }) {
  const ex = res.explanation!
  const feedback = useAiFeedback()
  const cited = bundle.filter((e) => (ex.evidence ?? []).includes(e.ref))
  return (
    <div className="ai-answer">
      <p>{ex.summary}</p>
      <div className="ai-grid">
        <div>
          <div className="k">
            <PartIcon part="cause" />
            Likely cause
          </div>
          <p>{ex.cause}</p>
        </div>
        <div>
          <div className="k">
            <PartIcon part="fix" />
            How to fix it
          </div>
          <p>{ex.fix}</p>
        </div>
      </div>
      <div className="k">
        <PartIcon part="evidence" />
        Based on
      </div>
      <div className="row ai-refs">
        {cited.map((e) => (
          <button key={e.ref} type="button" className="ai-ref" onClick={() => document.getElementById(evidenceId(e.ref))?.focus()}>
            {e.ref}
          </button>
        ))}
      </div>
      <EvidenceList items={cited} />
      <p className="ai-note">Based on the evidence above. Any change is shown for review first.</p>
      <Proposals items={res.proposals} source="ai-diagnosis" />
      <div className="row ai-feedback">
        {feedback.isSuccess ? (
          <span className="muted">Thanks.</span>
        ) : (
          <>
            <span className="muted">Was this right?</span>
            <Button size="sm" variant="ghost" onClick={() => feedback.mutate({ feature: 'explain', ref: name, right: true })}>
              Yes
            </Button>
            <Button size="sm" variant="ghost" onClick={() => feedback.mutate({ feature: 'explain', ref: name, right: false })}>
              No
            </Button>
          </>
        )}
      </div>
    </div>
  )
}

function Preview({ name }: { name: string }) {
  const preview = useExplain(name)
  const { mutate } = preview
  useEffect(() => mutate(true), [mutate])
  const b = preview.data?.bundle
  return (
    <div className="ai-preview">
      {preview.isError && <p role="alert">{problemText(preview.error)}</p>}
      {b && (
        <>
          {(b.notes ?? []).map((n) => (
            <p key={n} className="muted">
              {n}
            </p>
          ))}
          {b.text && <pre className="evidence">{b.text}</pre>}
        </>
      )}
    </div>
  )
}

/**
 * "Explain more": the model's reading of the evidence, as plain text, under
 * the rules' finding. Shown only when AI is on. When the rules could not
 * pin the cause down (known false) it opens and asks at once.
 */
export function AiCard({ name, known, bundle }: { name: string; known: boolean; bundle: Evidence[] }) {
  const model = useAiStatus().data?.model
  const explain = useExplain(name)
  const [open, setOpen] = useState(!known)
  const { mutate } = explain
  const ask = () => mutate(false)
  const asked = useRef(false)
  useEffect(() => {
    if (open && !asked.current) {
      asked.current = true
      mutate(false)
    }
  }, [open, mutate])
  const res = explain.data
  const left = useCountdown(res?.retryAfter ?? 0, ask)
  return (
    <Card className="pad ai-card" aria-label="AI explanation">
      <div className="row ai-head">
        <span className="label">
          <Sparkles size={15} aria-hidden="true" />
          {open ? 'AI explanation' : 'Want a closer reading of the evidence?'}
        </span>
        {open && model && <span className="faint ai-model">{model}</span>}
        {!open && (
          <Button size="sm" icon={Sparkles} onClick={() => setOpen(true)}>
            Explain more
          </Button>
        )}
      </div>
      {open && (
        <div className="ai-body">
          {explain.isPending && <p className="muted">Reading the evidence</p>}
          {explain.isError && (
            <p role="alert">
              {problemText(explain.error)}{' '}
              <Button size="sm" onClick={ask}>
                Retry
              </Button>
            </p>
          )}
          {res?.explanation && <Answer res={res} bundle={bundle} name={name} />}
          {res && !res.explanation && res.message && (
            <div className="ai-message" role="status">
              <p>{res.message}</p>
              {(res.retryAfter ?? 0) > 0 ? (
                <p className="muted">{left > 0 ? `${left} s` : ''}</p>
              ) : (
                <Button size="sm" onClick={ask}>
                  Retry
                </Button>
              )}
            </div>
          )}
          <More closed="What will be sent" open="Hide what will be sent">
            <Preview name={name} />
          </More>
        </div>
      )}
    </Card>
  )
}
