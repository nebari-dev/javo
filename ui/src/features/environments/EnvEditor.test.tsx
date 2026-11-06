import { fireEvent, render, screen } from '@testing-library/react'
import { useState } from 'react'
import { MemoryRouter } from 'react-router'
import { describe, expect, it } from 'vitest'
import { EnvEditor, envBody, envChanged, rowsOf, type EnvRow } from './EnvEditor'
import { choose } from '../../test/select'

function Harness({
  initial = [] as EnvRow[],
  errors = {} as Record<string, string>,
  available = ['OPENAI_KEY', 'DB_PASSWORD'],
  onRows,
}: {
  initial?: EnvRow[]
  errors?: Record<string, string>
  available?: string[]
  onRows?: (r: EnvRow[]) => void
}) {
  const [rows, setRows] = useState(initial)
  return (
    <EnvEditor
      rows={rows}
      available={available}
      errors={errors}
      hint="The app gets each as an environment variable."
      onChange={(r) => {
        setRows(r)
        onRows?.(r)
      }}
    />
  )
}

const row = (r: Partial<EnvRow>): EnvRow => ({ name: '', nameTyped: false, kind: 'value', value: '', secret: '', ...r })

describe('envBody', () => {
  it('sends typed values and secrets, a secret named after itself unless a name was typed, and drops empty rows', () => {
    expect(
      envBody([
        row({ name: ' LOG_LEVEL ', nameTyped: true, value: 'debug' }),
        row({ kind: 'secret', secret: 'OPENAI_KEY' }),
        row({}),
        row({ kind: 'secret', name: 'PGPASSWORD', nameTyped: true, secret: 'DB_PASSWORD' }),
        row({ name: 'EMPTY', nameTyped: true }),
      ]),
    ).toEqual([
      { name: 'LOG_LEVEL', value: 'debug' },
      { name: 'OPENAI_KEY', secret: 'OPENAI_KEY' },
      { name: 'PGPASSWORD', secret: 'DB_PASSWORD' },
      { name: 'EMPTY', value: '' },
    ])
  })

  it('round trips what an app has, in any order', () => {
    const now = [
      { name: 'PGPASSWORD', secret: 'DB_PASSWORD' },
      { name: 'LOG_LEVEL', value: 'debug' },
    ]
    expect(envBody(rowsOf(now))).toEqual(now)
    expect(envChanged(rowsOf(now).reverse(), now)).toBe(false)
    expect(envChanged([...rowsOf(now), row({ name: 'X', nameTyped: true })], now)).toBe(true)
  })
})

describe('EnvEditor', () => {
  it('takes a typed value, then a secret named after itself until a name is typed', async () => {
    let last: EnvRow[] = []
    render(<Harness onRows={(r) => (last = r)} />)
    fireEvent.click(screen.getByRole('button', { name: 'Add a variable' }))
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'LOG_LEVEL' } })
    fireEvent.change(screen.getByLabelText('Value'), { target: { value: 'debug' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add a variable' }))
    await choose(screen.getAllByLabelText('From')[1], 'Secret')
    fireEvent.click(screen.getByRole('combobox', { name: 'Secret' }))
    fireEvent.click(screen.getByRole('option', { name: 'OPENAI_KEY' }))
    expect((screen.getAllByLabelText('Name')[1] as HTMLInputElement).value).toBe('OPENAI_KEY')
    fireEvent.change(screen.getAllByLabelText('Name')[1], { target: { value: 'OPENAI_API_KEY' } })
    expect(envBody(last)).toEqual([
      { name: 'LOG_LEVEL', value: 'debug' },
      { name: 'OPENAI_API_KEY', secret: 'OPENAI_KEY' },
    ])
    fireEvent.click(screen.getByRole('button', { name: 'Remove LOG_LEVEL' }))
    expect(envBody(last)).toEqual([{ name: 'OPENAI_API_KEY', secret: 'OPENAI_KEY' }])
  })

  it('links to Settings when there is no secret to pick', () => {
    render(
      <MemoryRouter>
        <Harness available={[]} initial={[row({ kind: 'secret' })]} />
      </MemoryRouter>,
    )
    expect(screen.getByRole('link', { name: 'Add one in Settings' }).getAttribute('href')).toBe('/settings/secrets?secret=new')
  })

  it('shows the server field errors on the row they name', () => {
    render(
      <Harness
        initial={[row({ name: 'OK', nameTyped: true, value: '1' }), row({ name: 'PORT', nameTyped: true, value: '2' })]}
        errors={{ 'env[1].name': 'PORT is set by the platform. Pick another name.' }}
      />,
    )
    const inputs = screen.getAllByLabelText('Name')
    expect(inputs[0].getAttribute('aria-invalid')).toBeNull()
    expect(inputs[1].getAttribute('aria-invalid')).toBe('true')
    expect(screen.getByText('PORT is set by the platform. Pick another name.')).toBeTruthy()
  })
})
