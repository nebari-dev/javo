import { useId } from 'react'
import { useNavigate } from 'react-router'
import type { Template } from '../../api'
import { KindLogo } from '../../components/KindLogo'
import { PartTitle } from '../../components/Section'
import { useTemplates } from '../../lib/queries'
import { engineKind } from './format'

function SampleCard({ template }: { template: Template }) {
  const navigate = useNavigate()
  const sample = template.jobs?.sample
  if (!sample) return null
  return (
    <div className="card sample-card" data-template={template.name}>
      <button
        type="button"
        aria-label={`Try a sample job: ${sample.title}`}
        onClick={() => navigate(`/jobs/new?sample=${encodeURIComponent(template.name)}`)}
      >
        <KindLogo kind={engineKind('command')} />
        <span>
          <b className="block">{sample.title}</b>
          <span className="muted small block">{template.displayName}</span>
        </span>
      </button>
    </div>
  )
}

/**
 * "Try a sample job": one card per job template that ships a sample. A click
 * opens New job filled in from the sample; nothing is made until Create job.
 * Nothing when no template has one.
 */
export function JobSamples() {
  const headId = useId()
  const withSample = (useTemplates().data ?? []).filter((t) => t.jobs?.sample)
  if (withSample.length === 0) return null
  return (
    <section className="section" aria-labelledby={headId}>
      <div className="section-head">
        <PartTitle part="samples" id={headId}>
          Try a sample job
        </PartTitle>
        <span className="sub">A ready-made job to start from</span>
      </div>
      <div className="grid choices">
        {withSample.map((t) => (
          <SampleCard key={t.name} template={t} />
        ))}
      </div>
    </section>
  )
}
