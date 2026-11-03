import { describe, expect, it } from 'vitest'
import indexHtml from '../../index.html?raw'

// Vite copies public/ to the site root, so href="/x" needs public/x.
const publicFiles = Object.keys(import.meta.glob('../../public/*', { query: '?raw' }))

describe('favicon', () => {
  it('links an SVG icon, a PNG fallback and the /favicon.ico browsers fetch on their own, each shipped with the build', () => {
    const links = [...indexHtml.matchAll(/<link[^>]+rel="(?:icon|apple-touch-icon)"[^>]*>/g)].map((m) => /href="([^"]+)"/.exec(m[0])?.[1])
    expect(links).toContain('/favicon.svg')
    expect(links).toContain('/favicon.ico')
    expect(links.some((h) => h?.endsWith('.png'))).toBe(true)
    for (const href of links) expect(publicFiles).toContain(`../../public${href}`)
  })
})
