import { describe, expect, it } from 'vitest'
import themeCss from './theme.css?raw'

// The custom properties of the first rule whose selector matches.
function tokens(selector: RegExp): Record<string, string> {
  const m = selector.exec(themeCss)
  if (!m) throw new Error(`no rule ${selector}`)
  const body = themeCss.slice(m.index + m[0].length, themeCss.indexOf('}', m.index + m[0].length))
  return Object.fromEntries([...body.matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((d) => [d[1], d[2].trim()]))
}

// WCAG 2 relative luminance and contrast ratio of two #rrggbb colours.
function luminance(hex: string): number {
  const m = /^#([0-9a-f]{6})$/i.exec(hex)
  if (!m) throw new Error(`not a #rrggbb colour: ${hex}`)
  const [r, g, b] = [0, 2, 4].map((i) => {
    const c = parseInt(m[1].slice(i, i + 2), 16) / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r + 0.7152 * g + 0.0722 * b
}

function contrast(a: string, b: string): number {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x)
  return (hi + 0.05) / (lo + 0.05)
}

const themes = {
  light: tokens(/^:root\{/m),
  'dark (system)': tokens(/:root:not\(\[data-theme="light"\]\)\{/),
  'dark (chosen)': tokens(/:root\[data-theme="dark"\]\{/),
}

// The surfaces a log line sits on: the log itself and the tint behind error and warning rows.
const SURFACES = ['--log-bg', '--log-err-bg', '--log-warn-bg']

describe('log colours', () => {
  it('both dark rules set the same log colours', () => {
    const log = (t: Record<string, string>) => Object.entries(t).filter(([k]) => k.startsWith('--log-'))
    expect(log(themes['dark (system)'])).toEqual(log(themes['dark (chosen)']))
  })

  for (const [theme, t] of Object.entries(themes)) {
    const colours = ['--text', ...Object.keys(t).filter((k) => k.startsWith('--log-') && !SURFACES.includes(k) && k !== '--log-head')]
    it(`every log colour reads at WCAG AA (4.5:1) on each log surface in the ${theme} theme`, () => {
      expect(colours.length).toBeGreaterThan(10)
      const low: string[] = []
      for (const s of SURFACES) {
        for (const c of colours) {
          const r = contrast(t[c], t[s])
          if (r < 4.5) low.push(`${c} ${t[c]} on ${s} ${t[s]}: ${r.toFixed(2)}`)
        }
      }
      expect(low).toEqual([])
    })
  }
})
