import { render } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import templateTypes from '../../../internal/model/template.go?raw'
import { KindLogo } from './KindLogo'

// The kind enum as the Go validation checks it: templateKinds in the model.
const KINDS = [.../templateKinds = \[\]string\{([^}]*)\}/.exec(templateTypes)![1].matchAll(/"([^"]+)"/g)].map((m) => m[1])

describe('KindLogo', () => {
  it('reads the full kind enum from the model', () => {
    expect(KINDS).toContain('jupyterlab')
    expect(KINDS).toContain('custom')
    expect(KINDS.length).toBeGreaterThanOrEqual(12)
  })

  // Kinds that are not a project have a line icon instead of a logo.
  const ICON_KINDS = ['custom', 'container', 'static']

  it.each(KINDS.filter((k) => !ICON_KINDS.includes(k)))('draws the project logo for the %s kind', (kind) => {
    const { container } = render(<KindLogo kind={kind} />)
    expect(container.querySelector('svg.lucide')).toBeNull()
    expect(container.querySelector('.mark')).not.toBeNull()
  })

  it('draws a container line icon for the container kind', () => {
    const { container } = render(<KindLogo kind="container" />)
    expect(container.querySelector('svg.lucide-container')).not.toBeNull()
  })

  it('draws a globe line icon for the static kind', () => {
    const { container } = render(<KindLogo kind="static" />)
    expect(container.querySelector('svg.lucide-globe')).not.toBeNull()
  })

  it.each(['custom', 'not-a-kind'])('draws the neutral terminal icon for %s', (kind) => {
    const { container } = render(<KindLogo kind={kind} />)
    expect(container.querySelector('svg.lucide-square-terminal')).not.toBeNull()
  })
})
