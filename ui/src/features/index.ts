import type { LucideIcon } from 'lucide-react'
import type { ComponentType } from 'react'
import type { RouteObject } from 'react-router'
import { filesFeature } from './files'
import { jobsFeature } from './jobs'
import { terminalFeature } from './terminal'

interface NavEntry {
  to: string
  label: string
  icon: LucideIcon
}

/** One line of Home's "Needs your attention", whichever feature it comes from. */
export interface AttentionEntry {
  key: string
  to: string
  name: string
  note?: string
  failed: boolean
  action: string
}

/** What a feature adds to the shell: its pages, a nav entry, Home sections and attention items. */
export interface FeatureModule {
  id: string
  routes: RouteObject[]
  nav?: NavEntry
  homeSections?: ComponentType[]
  /** A hook, called on every render of Home in the same order. */
  useAttention?: () => AttentionEntry[]
}

/** Static: a feature exists in the UI when it is listed here. */
export const features: FeatureModule[] = [jobsFeature, filesFeature, terminalFeature]

export function useFeatureAttention(): AttentionEntry[] {
  return features.flatMap((f) => f.useAttention?.() ?? [])
}
