import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { Template } from '../../api'
import { location, mockApi, renderApp } from '../../test/api'
import { me } from '../../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

function tpl(kind: string, displayName: string, withSample = true): Template {
  const t: Template = { name: kind, displayName, description: `${displayName} app`, category: 'other', kind, fileExtensions: [], maxSharing: 'Private', apiAccessDefault: 'None', maxApiAccess: 'ReadWrite', basePath: 'Prefix', appLogin: 'Disabled', userCommand: 'None', userImage: false, fromRepo: 'None', port: 8080 }
  return withSample ? { ...t, sample: { title: `${displayName} tour`, file: `samples/${kind}`, command: '' } } : t
}

// Display names sort alphabetically after the popular kinds: alpha, bravo, ...
const twelve = [
  tpl('zeta', 'Zeta'),
  tpl('bokeh', 'Bokeh'),
  tpl('streamlit', 'Streamlit'),
  tpl('marimo', 'Marimo'),
  tpl('jupyterlab', 'JupyterLab'),
  tpl('voila', 'Voila'),
  tpl('dash', 'Dash'),
  tpl('panel', 'Panel'),
  tpl('gradio', 'Gradio'),
  tpl('alpha', 'Alpha'),
  tpl('fastapi', 'FastAPI'),
  tpl('rstudio', 'RStudio'),
]

function home(items: Template[], extra: Record<string, unknown> = {}) {
  return mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/templates': { body: { items } },
    ...extra,
  })
}

async function gallery() {
  return within(await screen.findByRole('region', { name: 'Try a sample' }))
}

describe('SampleGallery on Home', () => {
  it('shows the six popular kinds in order and the rest behind a More control', async () => {
    home(twelve)
    renderApp('/')
    const g = await gallery()
    const names = g.getAllByRole('button', { name: / sample: / }).map((b) => (b.closest('[data-template]') as HTMLElement).getAttribute('data-template'))
    expect(names).toEqual(['jupyterlab', 'streamlit', 'panel', 'dash', 'gradio', 'voila'])
    await userEvent.click(g.getByRole('button', { name: '6 more samples' }))
    expect(g.getByRole('button', { name: 'Hide 6 samples' }).getAttribute('aria-expanded')).toBe('true')
    const all = g.getAllByRole('button', { name: / sample: / }).map((b) => (b.closest('[data-template]') as HTMLElement).getAttribute('data-template'))
    expect(all.slice(6)).toEqual(['alpha', 'bokeh', 'fastapi', 'marimo', 'rstudio', 'zeta'])
  })

  it('has no card for a template without a sample, and no section when none has one', async () => {
    home([tpl('jupyterlab', 'JupyterLab'), tpl('streamlit', 'Streamlit', false)])
    renderApp('/')
    const g = await gallery()
    expect(g.getAllByRole('button', { name: / sample: / })).toHaveLength(1)
    expect(g.queryByText('Streamlit')).toBeNull()
  })

  it('omits the section when no template has a sample', async () => {
    home([tpl('jupyterlab', 'JupyterLab', false)])
    renderApp('/')
    await screen.findByRole('heading', { name: 'No apps yet' })
    expect(screen.queryByRole('region', { name: 'Try a sample' })).toBeNull()
  })

  it('opens New app with an image sample filled in for a template that runs images', async () => {
    const container: Template = {
      ...tpl('container', 'Container image', false),
      userImage: true,
      sample: { title: 'Toolbox', file: '', command: '', image: 'example.com/toolbox:1', port: 9000 },
    }
    home([container])
    renderApp('/')
    const g = await gallery()
    await userEvent.click(g.getByRole('button', { name: /Container image sample: Toolbox/ }))
    await waitFor(() => expect(location()).toBe('/apps/new?template=container&sample=1'))
    expect(((await screen.findByLabelText('Image')) as HTMLInputElement).value).toBe('example.com/toolbox:1')
  })

  it('opens New app with the template and its sample already chosen, without creating anything', async () => {
    const api = home(twelve)
    renderApp('/')
    const g = await gallery()
    await userEvent.click(g.getByRole('button', { name: /Streamlit/ }))
    await waitFor(() => expect(location()).toBe('/apps/new?template=streamlit'))
    expect(await screen.findByRole('button', { name: 'Streamlit, change' })).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Code: Home folder · Sample: Streamlit tour, change' })).toBeTruthy()
    expect((screen.getByRole('textbox', { name: 'Name' }) as HTMLInputElement).value).toBe('Streamlit app')
    expect(api.count('POST', '/api/samples/streamlit')).toBe(0)
  })
})
