import type { PaletteItem, PaletteSection } from './Palette'
import { useJobsPalette } from '../jobs/palette'

// What features add to the command palette. Kept apart from the feature
// modules so the palette's code loads only when it is opened.
const hooks: (() => { section?: PaletteSection; actions?: PaletteItem[] })[] = [useJobsPalette]

/** Every feature's palette section and create actions, in feature order. Static list, so the hooks run in the same order every render. */
export function useFeaturePalette(): { sections: PaletteSection[]; actions: PaletteItem[] } {
  const parts = hooks.map((h) => h())
  return { sections: parts.flatMap((p) => (p.section ? [p.section] : [])), actions: parts.flatMap((p) => p.actions ?? []) }
}
