import {
  Activity,
  Archive,
  ArchiveRestore,
  Bell,
  Box,
  Braces,
  CalendarClock,
  ChartColumn,
  CircleAlert,
  ClipboardCheck,
  Clock3,
  Cpu,
  Database,
  DatabaseBackup,
  FileCode2,
  FlaskConical,
  Folders,
  GitPullRequest,
  HardDrive,
  Home,
  Hourglass,
  KeyRound,
  LifeBuoy,
  Layers2,
  LayoutDashboard,
  LayoutGrid,
  LayoutTemplate,
  Lock,
  LockKeyhole,
  MoonStar,
  Package,
  RotateCcw,
  Scale,
  ScrollText,
  Server,
  Settings,
  SlidersHorizontal,
  Sparkles,
  Terminal,
  Timer,
  Users,
  Variable,
  Workflow,
  Wrench,
  type LucideIcon,
} from 'lucide-react'
import { createContext, useContext, useId, type ElementType, type ReactNode } from 'react'

/**
 * The one icon of each concept, wherever it is named: the sidebar, a tab, a
 * heading, a card's title, or a row of what was asked for. The same concept
 * always gets the same icon.
 */
export const PART_ICONS = {
  // Pages and areas.
  home: Home,
  apps: LayoutGrid,
  templates: LayoutTemplate,
  jobs: Clock3,
  settings: Settings,
  storage: HardDrive,
  attention: CircleAlert,
  samples: FlaskConical,
  // An app's page.
  overview: LayoutDashboard,
  sharing: Users,
  private: Lock,
  // Settings and admin.
  tokens: KeyRound,
  secrets: LockKeyhole,
  usage: ChartColumn,
  hours: Hourglass,
  people: Users,
  running: Activity,
  backups: DatabaseBackup,
  everything: Archive,
  database: Database,
  homes: Folders,
  restore: ArchiveRestore,
  // Parts of an app or a job, on the form that makes it and the page that shows it.
  app: LayoutGrid,
  code: FileCode2,
  start: Terminal,
  image: Box,
  previews: GitPullRequest,
  environment: Package,
  size: Server,
  advanced: SlidersHorizontal,
  apiAccess: KeyRound,
  variables: Variable,
  idle: MoonStar,
  parameters: Braces,
  schedule: CalendarClock,
  engine: Workflow,
  kernel: Cpu,
  overlap: Layers2,
  retries: RotateCcw,
  timeLimit: Timer,
  notify: Bell,
  ai: Sparkles,
  review: ClipboardCheck,
  // What went wrong, on a failure card and in an AI diagnosis.
  cause: CircleAlert,
  fix: Wrench,
  blame: Scale,
  help: LifeBuoy,
  evidence: ScrollText,
} satisfies Record<string, LucideIcon>

export type Part = keyof typeof PART_ICONS

/** The icon of a part, sized for a line of text; hidden from screen readers, so names do not change. */
export function PartIcon({ part, size = 15 }: { part: Part; size?: number }) {
  const Icon = PART_ICONS[part]
  return <Icon size={size} aria-hidden="true" className="part-icon" />
}

/**
 * A heading led by its part's icon, quiet like the sidebar's: a section's
 * name, a card's title, a tab. Its accessible name is the text alone.
 */
export function PartTitle({
  part,
  as: Tag = 'h2',
  id,
  className,
  children,
}: {
  part: Part
  as?: ElementType
  id?: string
  className?: string
  children: ReactNode
}) {
  return (
    <Tag id={id} className={className ? `part-title ${className}` : 'part-title'}>
      <PartIcon part={part} size={17} />
      {children}
    </Tag>
  )
}

const SectionHeading = createContext<string | undefined>(undefined)

/**
 * The id of the heading of the Section around a card, if any: a card in a
 * Section leaves its own title to the Section and labels its controls with
 * this id instead.
 */
export function useSectionHeading(): string | undefined {
  return useContext(SectionHeading)
}

/** One part of a form (App, Code, Environment...): a heading with the part's icon, then its card or row. */
export function Section({ part, title, children }: { part: Part; title: string; children: ReactNode }) {
  const id = useId()
  return (
    <section className="part" aria-labelledby={id}>
      <PartTitle part={part} id={id} className="part-h">
        {title}
      </PartTitle>
      <SectionHeading.Provider value={id}>{children}</SectionHeading.Provider>
    </section>
  )
}
