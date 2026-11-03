import { afterEach, describe, expect, it } from 'vitest'
import { mockApi } from '../test/api'
import { applyBrand, DEFAULT_BRAND, loadBrand } from './brand'

const icons = () => [...document.head.querySelectorAll('link[rel~="icon"],link[rel="apple-touch-icon"]')].map((l) => l.getAttribute('href'))

describe('brand', () => {
  afterEach(() => {
    document.head.innerHTML = ''
  })

  it('loads the branding the server answers', async () => {
    mockApi({ 'GET /api/branding': { body: { name: 'Acme Lab', accent: '#0a7d55' } } })
    expect(await loadBrand()).toEqual({ name: 'Acme Lab', accent: '#0a7d55' })
  })

  it('falls back to Javo when the server cannot answer', async () => {
    mockApi({ 'GET /api/branding': { status: 503 } })
    expect(await loadBrand()).toEqual(DEFAULT_BRAND)
  })

  it('sets the title, the favicon and the accent tokens', () => {
    document.head.innerHTML = '<link rel="icon" type="image/svg+xml" href="/favicon.svg"><link rel="apple-touch-icon" href="/favicon.png">'
    applyBrand({ name: 'Acme Lab', favicon: '/branding/favicon?v=1', accent: '#0a7d55' })
    expect(document.title).toBe('Acme Lab')
    expect(icons()).toEqual(['/branding/favicon?v=1'])
    const style = document.documentElement.style
    expect(style.getPropertyValue('--accent')).toBe('#0a7d55')
    expect(style.getPropertyValue('--accent-soft')).toContain('#0a7d55')
    expect(style.getPropertyValue('--accent-text')).toContain('#0a7d55')
  })

  it('keeps the built-in favicon and accent without branding', () => {
    document.head.innerHTML = '<link rel="icon" href="/favicon.svg">'
    applyBrand({ name: 'Acme Lab', accent: '#0a7d55' })
    applyBrand(DEFAULT_BRAND)
    expect(document.title).toBe('Javo')
    expect(icons()).toEqual(['/favicon.svg'])
    expect(document.documentElement.style.getPropertyValue('--accent')).toBe('')
  })
})
