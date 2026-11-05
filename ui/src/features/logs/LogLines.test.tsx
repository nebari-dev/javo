import { render, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { LogLines } from './LogLines'

const items = (name: string) => within(screen.getByRole('list', { name })).getAllByRole('listitem')

/** Waits until the lazily loaded highlighter has marked up the list. */
async function highlighted(name: string) {
  await waitFor(() => expect(screen.getByRole('list', { name }).querySelector('[class^="lk-"]')).not.toBeNull())
  return items(name)
}

describe('LogLines', () => {
  it('shows each line as plain text, so a line that looks like HTML is shown literally', async () => {
    const { container } = render(
      <LogLines
        label="App log"
        lines={[
          { time: '2026-10-30T12:00:01Z', text: '<script>alert(1)</script>' },
          { text: '<b>bold?</b> "quoted"' },
        ]}
      />,
    )
    const li = await highlighted('App log')
    expect(li).toHaveLength(2)
    expect(li[0].textContent).toContain('<script>alert(1)</script>')
    expect(li[1].textContent).toContain('<b>bold?</b> "quoted"')
    expect(container.querySelector('script, b')).toBeNull()
  })

  it('shows the reason and marks warnings', () => {
    render(
      <LogLines
        label="Events"
        lines={[
          { time: '2026-10-30T12:00:01Z', label: 'Pulling', text: 'Pulling image' },
          { time: '2026-10-30T12:00:02Z', label: 'BackOff', text: 'Back-off restarting', warn: true },
        ]}
      />,
    )
    const li = items('Events')
    expect(within(li[1]).getByText('BackOff')).toBeTruthy()
    expect(li.map((i) => i.dataset.level)).toEqual([undefined, 'warn'])
    expect(within(li[1]).getByText('Warning')).toBeTruthy()
  })

  it('marks each line with its level and colours its parts', async () => {
    render(
      <LogLines
        label="App log"
        lines={[
          { text: 'INFO:     Uvicorn running on http://0.0.0.0:8000 (Press CTRL+C to quit)' },
          { text: 'WARNING:  Invalid HTTP request received.' },
          { text: 'ERROR:    Exception in ASGI application' },
          { text: "[D 2026-10-30 12:00:04.567 ServerApp] Searching ['/home/alice'] for config files" },
        ]}
      />,
    )
    const li = await highlighted('App log')
    expect(li.map((i) => i.dataset.level)).toEqual(['info', 'warn', 'error', 'debug'])
    expect(li[0].querySelector('.lk-url')?.textContent).toBe('http://0.0.0.0:8000')
    expect(li[2].querySelector('.lk-error')?.textContent).toBe('ERROR')
    expect(within(li[2]).getByText('Error')).toBeTruthy()
  })

  it('keeps a stack trace under the error that started it, and stops at the next line of its own', async () => {
    render(
      <LogLines
        label="App log"
        lines={[
          { text: 'Traceback (most recent call last):' },
          { text: '  File "/home/alice/sales/app.py", line 3, in <module>' },
          { text: '    import plotly.express as px' },
          { text: "ModuleNotFoundError: No module named 'plotly'" },
          { text: 'panic: runtime error: invalid memory address or nil pointer dereference' },
          { text: '[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x47f1f3]' },
          { text: '' },
          { text: 'goroutine 1 [running]:' },
          { text: 'main.main()' },
          { text: '\t/app/main.go:12 +0x1d' },
          { text: 'exit status 2' },
          { text: '' },
        ]}
      />,
    )
    const li = await highlighted('App log')
    expect(li.map((i) => i.dataset.level)).toEqual([
      'error',
      'error',
      'error',
      'error',
      'error',
      'error',
      'error',
      'error',
      'error',
      'error',
      undefined,
      undefined,
    ])
    expect(li[1].querySelector('.lk-path')?.textContent).toBe('"/home/alice/sales/app.py"')
  })

  it('renders ANSI colours from the app instead of escape codes', async () => {
    // uvicorn colours the level name with click.style(fg="green").
    render(<LogLines label="App log" lines={[{ text: '\x1b[32mINFO\x1b[0m:     Uvicorn running on \x1b[1mhttp://0.0.0.0:8000\x1b[0m (Press CTRL+C to quit)' }]} />)
    const [li] = items('App log')
    await waitFor(() => expect(li.dataset.level).toBe('info'))
    expect(li.textContent).toBe('INFO:     Uvicorn running on http://0.0.0.0:8000 (Press CTRL+C to quit)')
    expect(li.querySelector('.ansi-green')?.textContent).toBe('INFO')
    expect(li.querySelector('.ansi-bold')?.textContent).toBe('http://0.0.0.0:8000')
    expect(li.dataset.level).toBe('info')
  })

  it('reads the level of an ANSI-coloured prefix and highlights the plain message after it', async () => {
    // Jupyter Server on a terminal: tornado's LogFormatter wraps the prefix in setaf 2 ... sgr0.
    render(<LogLines label="App log" lines={[{ text: '\x1b[32m[I 2026-10-30 12:00:01.234 ServerApp]\x1b(B\x1b[m Serving notebooks from local directory: /home/alice' }]} />)
    const [li] = await highlighted('App log')
    expect(li.textContent).toBe('[I 2026-10-30 12:00:01.234 ServerApp] Serving notebooks from local directory: /home/alice')
    expect(li.querySelector('.ansi-green')?.textContent).toBe('[I 2026-10-30 12:00:01.234 ServerApp]')
    expect(li.querySelector('.lk-path')?.textContent).toBe('/home/alice')
    expect(li.dataset.level).toBe('info')
  })
  it('shows bidi and invisible characters as visible markers, so a line cannot hide or reorder its text', async () => {
    render(
      <LogLines
        label="App log"
        lines={[
          { text: 'saved invoice\u202Efdp.exe' },
          { text: 'rm -rf\u200B /tmp/x\u2066' },
          { text: '\x1b[31mred\u202Dtext\x1b[0m' },
        ]}
      />,
    )
    const li = await highlighted('App log')
    expect(li[0].textContent).toBe('saved invoice<U+202E>fdp.exe')
    expect(li[1].textContent).toBe('rm -rf<U+200B> /tmp/x<U+2066>')
    expect(li[2].textContent).toBe('red<U+202D>text')
  })
})
