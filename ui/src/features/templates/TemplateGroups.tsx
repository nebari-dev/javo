import { useId, type ReactNode } from 'react'
import type { Template, TemplateCategory } from '../../api'
import { useTemplateCategories } from '../../lib/queries'

/** Kinds in the order their cards show within a category; other kinds follow by display name. */
const KIND_ORDER = [
  'jupyterlab', 'vscode', 'rstudio', 'marimo', 'terminal',
  'streamlit', 'panel', 'dash', 'gradio', 'voila', 'bokeh',
  'fastapi', 'static', 'node', 'go', 'custom', 'container',
  'claude-code', 'pi', 'herdr',
  'python', 'r', 'julia',
]

const OTHER: TemplateCategory = { id: 'other', title: 'Other' }

interface TemplateGroup extends TemplateCategory {
  templates: Template[]
}

/**
 * The templates under their categories, in the categories' order, leaving out
 * categories with no template. A template whose category is not listed goes
 * under Other.
 */
export function groupTemplates(templates: Template[], categories: TemplateCategory[]): TemplateGroup[] {
  const rank = (t: Template) => {
    const i = KIND_ORDER.indexOf(t.kind)
    return i < 0 ? KIND_ORDER.length : i
  }
  const sorted = [...templates].sort((a, b) => rank(a) - rank(b) || a.displayName.localeCompare(b.displayName))
  const listed = new Set(categories.map((c) => c.id))
  const all = listed.has(OTHER.id) ? categories : [...categories, OTHER]
  const of = (t: Template) => (listed.has(t.category) ? t.category : OTHER.id)
  return all.map((c) => ({ ...c, templates: sorted.filter((t) => of(t) === c.id) })).filter((g) => g.templates.length > 0)
}

/**
 * One labelled group per category, each a grid of the cards card renders
 * (keyed by the caller). heading makes the labels page headings (the
 * Templates page); without it they only name their group (inside New app's
 * card, under its own heading).
 */
export function TemplateGroups({ templates, card, heading = false }: { templates: Template[]; card: (t: Template) => ReactNode; heading?: boolean }) {
  const categories = useTemplateCategories().data ?? []
  const base = useId()
  const Label = heading ? 'h2' : 'span'
  return (
    <div className="tpl-groups">
      {groupTemplates(templates, categories).map((g) => (
        <div key={g.id} role="group" aria-labelledby={`${base}-${g.id}`} className="tpl-group">
          <Label id={`${base}-${g.id}`} className="label">
            {g.title}
          </Label>
          <div className="grid choices">{g.templates.map(card)}</div>
        </div>
      ))}
    </div>
  )
}
