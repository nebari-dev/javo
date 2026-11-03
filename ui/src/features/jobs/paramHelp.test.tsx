import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ParamHelp } from './paramHelp'

describe('ParamHelp', () => {
  it('tells a marimo notebook to read its values with mo.cli_args()', () => {
    render(<ParamHelp engine="marimo" param={{ name: 'region', type: 'string', default: 'emea' }} />)
    expect(screen.getByText(/--region emea/)).toBeTruthy()
    expect(screen.getByText('region = mo.cli_args().get("region")')).toBeTruthy()
  })

  it('tells a Quarto document to tag a cell parameters', () => {
    render(<ParamHelp engine="quarto" param={{ name: 'ok', type: 'boolean', default: 'true' }} />)
    expect(screen.getByText(/#\| tags: \[parameters\]\s+ok = True/)).toBeTruthy()
  })

  it('asks for a file before it can say how the values arrive', () => {
    render(<ParamHelp param={{ name: 'region', type: 'string', default: 'emea' }} />)
    expect(screen.getByText(/Choose a file/)).toBeTruthy()
  })
})
