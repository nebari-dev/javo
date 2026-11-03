import { useId } from 'react'
import { useNavigate } from 'react-router'
import type { Template } from '../../api'
import { count } from '../../lib/plural'
import { KindLogo } from '../../components/KindLogo'
import { More } from '../../components/More'
import { PartTitle } from '../../components/Section'

/** Kinds shown first, in this order; every other kind follows by display name. */
const POPULAR_KINDS = ['jupyterlab', 'streamlit', 'panel', 'dash', 'gradio', 'voila']
const SHOWN = 6

/** Templates in POPULAR_KINDS order, then the rest by display name. */
export function popularFirst(templates: Template[]): Template[] {
  const rank = (t: Template) => {
    const i = POPULAR_KINDS.indexOf(t.kind)
    return i < 0 ? POPULAR_KINDS.length : i
  }
  return [...templates].sort((a, b) => rank(a) - rank(b) || a.displayName.localeCompare(b.displayName))
}

/**
 * "Try a sample": one card per template that ships a sample. A click opens New
 * app with that template, its sample (for an image template, the sample's
 * image, port and command) and a default name filled in, so Create is the only
 * other click.
 * Renders nothing when no template has a sample.
 */
export function SampleGallery({ templates }: { templates: Template[] }) {
  const headId = useId()
  const navigate = useNavigate()
  const list = popularFirst(templates.filter((t) => t.sample))
  if (list.length === 0) return null

  const cards = (ts: Template[]) => (
    <div className="grid choices">
      {ts.map((t) => (
        <div key={t.name} className="card sample-card" data-template={t.name}>
          <button
            type="button"
            aria-label={`${t.displayName} sample: ${t.sample?.title}`}
            onClick={() => navigate(`/apps/new?template=${encodeURIComponent(t.name)}${t.userImage ? '&sample=1' : ''}`)}
          >
            <KindLogo kind={t.kind} />
            <span>
              <b className="block">{t.displayName}</b>
              <span className="muted small block">{t.sample?.title}</span>
            </span>
          </button>
        </div>
      ))}
    </div>
  )

  const rest = list.slice(SHOWN)
  return (
    <section className="section" aria-labelledby={headId}>
      <div className="section-head">
        <PartTitle part="samples" id={headId}>
          Try a sample
        </PartTitle>
        <span className="sub">Starts with example files, no setup</span>
      </div>
      {cards(list.slice(0, SHOWN))}
      {rest.length > 0 && (
        <More closed={count(rest.length, 'more sample')} open={`Hide ${count(rest.length, 'sample')}`}>
          {cards(rest)}
        </More>
      )}
    </section>
  )
}
