import { BookOpen, CircleStop, ExternalLink, Monitor, Moon, Play, Plus, Sun, type LucideIcon } from 'lucide-react'
import { useState } from 'react'
import { useNavigate } from 'react-router'
import type { SharedApp, Template, Workspace } from '../../api'
import { KindLogo } from '../../components/KindLogo'
import { PART_ICONS } from '../../components/Section'
import { PaletteAction, PalettePanel, usePalette, type PaletteItem, type PaletteSection } from './Palette'
import { Pill } from '../../components/Pill'
import { StopConfirm } from '../apps/StopButton'
import { popularFirst } from '../templates/SampleGallery'
import { groupTemplates } from '../templates/TemplateGroups'
import { features } from '..'
import { useFeaturePalette } from './featurePalette'
import { useServerInfo, useShared, useStartStop, useTemplateCategories, useTemplates, useWorkspaces } from '../../lib/queries'
import { appLink, pillStatus } from '../apps/status'
import { setTheme, useTheme, type Theme } from '../../lib/theme'

/** A line icon in the palette's icon column. */
const glyph = (Icon: LucideIcon) => <Icon size={16} />

const openInNewTab = (href: string) => window.open(href, '_blank', 'noopener')

function OpenInNewTab({ href }: { href: string }) {
  const close = usePalette()
  return (
    <PaletteAction
      icon={ExternalLink}
      label="Open in new tab"
      onSelect={() => {
        close()
        openInNewTab(href)
      }}
    />
  )
}

/** Open, Start or Stop, by the same rules as the app's card; Stop asks first. */
function AppActions({ ws }: { ws: Workspace }) {
  const close = usePalette()
  const navigate = useNavigate()
  const act = useStartStop(ws.name)
  const [asking, setAsking] = useState(false)
  const href = ws.phase === 'Running' ? appLink(ws.url) : undefined
  return (
    <>
      {href && <OpenInNewTab href={href} />}
      {ws.phase === 'Stopped' && (
        <PaletteAction
          icon={Play}
          label="Start"
          onSelect={() =>
            act.mutate('start', {
              onSuccess: () => {
                close()
                void navigate(`/apps/${encodeURIComponent(ws.name)}`)
              },
            })
          }
        />
      )}
      {ws.phase !== 'Stopped' && ws.phase !== 'Stopping' && (
        <PaletteAction icon={CircleStop} label={ws.phase === 'Failed' ? 'Stop retrying' : 'Stop'} onSelect={() => setAsking(true)} />
      )}
      {asking && (
        <PalettePanel>
          <StopConfirm ws={ws} act={act} onDone={close} onCancel={() => setAsking(false)} />
        </PalettePanel>
      )}
    </>
  )
}

function appItem(ws: Workspace, go: (to: string) => void): PaletteItem {
  return {
    id: `app:${ws.name}`,
    label: ws.displayName,
    keywords: [ws.name, ws.kind, ws.template],
    icon: <KindLogo kind={ws.kind} />,
    meta: <Pill status={pillStatus(ws.phase)} />,
    run: () => go(`/apps/${encodeURIComponent(ws.name)}`),
    actions: <AppActions ws={ws} />,
  }
}

// Viewers have no page for an app they were shared, so it opens the app itself, once it runs.
function sharedItem(app: SharedApp): PaletteItem {
  const href = app.phase === 'Running' ? appLink(app.url) : undefined
  return {
    id: `shared:${app.owner.id}/${app.name}`,
    label: app.displayName,
    detail: `from ${app.owner.displayName}`,
    keywords: [app.owner.displayName, app.kind, app.template],
    icon: <KindLogo kind={app.kind} />,
    meta: <Pill status={pillStatus(app.phase)} />,
    run: href ? () => openInNewTab(href) : undefined,
    actions: href ? <OpenInNewTab href={href} /> : undefined,
  }
}

// A new app starts from the template's sample unless told otherwise, so "New X app" says so.
function templateItems(t: Template, go: (to: string) => void): PaletteItem[] {
  const to = `/apps/new?template=${encodeURIComponent(t.name)}`
  const items: PaletteItem[] = [
    {
      id: `new:${t.name}`,
      label: `New ${t.displayName} app`,
      keywords: [t.kind, 'create'],
      icon: <KindLogo kind={t.kind} />,
      meta: <span className="pal-note">{t.description}</span>,
      run: () => go(t.sample ? `${to}&sample=none` : to),
    },
  ]
  if (t.sample)
    items.push({
      id: `sample:${t.name}`,
      label: `Try the ${t.displayName} sample`,
      keywords: [t.sample.title, t.kind, 'example'],
      icon: <KindLogo kind={t.kind} />,
      meta: <span className="pal-note">{t.sample.title}</span>,
      run: () => go(to),
    })
  return items
}

/** One Create item per template category, its first template as New app lists them, in the categories' order. */
export function useCreateItems(): PaletteItem[] {
  const navigate = useNavigate()
  const templates = (useTemplates().data ?? []).filter((t) => !t.jobOnly)
  const categories = useTemplateCategories().data ?? []
  return groupTemplates(templates, categories).map(({ templates: [t], title }) => ({
    id: `create:${t.name}`,
    label: `Create ${t.displayName} app`,
    icon: <KindLogo kind={t.kind} />,
    meta: <span className="pal-note">{title}</span>,
    run: () => void navigate(`/apps/new?template=${encodeURIComponent(t.name)}`),
  }))
}

const THEMES: { value: Theme; label: string; icon: LucideIcon }[] = [
  { value: 'light', label: 'Light theme', icon: Sun },
  { value: 'dark', label: 'Dark theme', icon: Moon },
  { value: 'system', label: 'System theme', icon: Monitor },
]

/** Everything the palette can find, from the queries the pages already use. */
export function usePaletteSections(): PaletteSection[] {
  const navigate = useNavigate()
  const go = (to: string) => void navigate(to)
  const mine = useWorkspaces().data ?? []
  const shared = useShared().data?.items ?? []
  // Job-only templates run jobs, never apps.
  const templates = popularFirst((useTemplates().data ?? []).filter((t) => !t.jobOnly))
  const apiDocs = useServerInfo().data?.apiDocs ?? false
  const theme = useTheme()
  const feature = useFeaturePalette()

  const page = (id: string, label: string, icon: LucideIcon, to: string): PaletteItem => ({ id: `go:${id}`, label, icon: glyph(icon), run: () => go(to) })
  const goTo: PaletteItem[] = [
    page('home', 'Home', PART_ICONS.home, '/'),
    page('apps', 'Apps', PART_ICONS.apps, '/apps'),
    page('templates', 'Templates', PART_ICONS.templates, '/templates'),
    ...features.flatMap((f) => (f.nav ? [page(f.id, f.nav.label, f.nav.icon, f.nav.to)] : [])),
    page('settings', 'Settings', PART_ICONS.settings, '/settings'),
  ]
  if (apiDocs)
    goTo.push({
      id: 'go:api-docs',
      label: 'API docs',
      keywords: ['openapi', 'reference'],
      icon: glyph(BookOpen),
      meta: <span className="pal-note">New tab</span>,
      run: () => openInNewTab('/api/docs/'),
    })

  const actions: PaletteItem[] = [
    { id: 'do:new-app', label: 'New app', keywords: ['create'], icon: glyph(Plus), run: () => go('/apps/new') },
    ...feature.actions,
    { id: 'do:new-token', label: 'New API token', keywords: ['create', 'key'], icon: glyph(PART_ICONS.tokens), run: () => go('/settings?token=new') },
    { id: 'do:new-secret', label: 'New secret', keywords: ['create', 'password', 'environment', 'variable'], icon: glyph(PART_ICONS.secrets), run: () => go('/settings/secrets?secret=new') },
    ...THEMES.map((t) => ({
      id: `theme:${t.value}`,
      label: t.label,
      keywords: ['appearance', 'mode', 'switch theme'],
      icon: glyph(t.icon),
      meta: theme === t.value ? <span className="pal-note">Current</span> : undefined,
      run: () => setTheme(t.value),
    })),
  ]

  return [
    { heading: 'Apps', items: [...mine.map((w) => appItem(w, go)), ...shared.map(sharedItem)] },
    ...feature.sections,
    { heading: 'Templates', items: templates.flatMap((t) => templateItems(t, go)) },
    { heading: 'Go to', items: goTo },
    { heading: 'Actions', items: actions },
  ]
}
