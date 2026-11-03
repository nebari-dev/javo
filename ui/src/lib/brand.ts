import { getBranding, type BrandingView } from '../api'
// Configures the API client (base URL, errors) before the first request.
import './http'

/** The install's white-label look, from GET /api/branding. */
export type Brand = BrandingView

/** The built-in look: the Javo name, mark, favicon and accent. */
export const DEFAULT_BRAND: Brand = { name: 'Javo' }

let current: Brand = DEFAULT_BRAND

/** The look in use; set once at start, before the first render. */
export function brand(): Brand {
  return current
}

/** Asks the server for the branding; anything but an answer keeps the built-in look. */
export async function loadBrand(): Promise<Brand> {
  try {
    const { data } = await getBranding()
    return data?.name ? data : DEFAULT_BRAND
  } catch {
    return DEFAULT_BRAND
  }
}

const ACCENT_TOKENS = ['--accent', '--accent-soft', '--accent-text']

/** Uses b for the title, favicon and accent tokens, and for every later brand() call. */
export function applyBrand(b: Brand): void {
  current = b
  document.title = b.name
  const root = document.documentElement.style
  if (b.accent) {
    // One colour for both themes; the soft and text shades mix it with the theme's own surface and text.
    root.setProperty('--accent', b.accent)
    root.setProperty('--accent-soft', `color-mix(in srgb, ${b.accent} 14%, var(--surface))`)
    root.setProperty('--accent-text', `color-mix(in srgb, ${b.accent} 80%, var(--text))`)
  } else {
    for (const t of ACCENT_TOKENS) root.removeProperty(t)
  }
  if (b.favicon) {
    document.head.querySelectorAll('link[rel~="icon"],link[rel="apple-touch-icon"]').forEach((l) => l.remove())
    const link = document.createElement('link')
    link.rel = 'icon'
    link.href = b.favicon
    document.head.append(link)
  }
}
