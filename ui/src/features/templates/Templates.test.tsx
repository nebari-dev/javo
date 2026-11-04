import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { Template } from '../../api'
import { mockApi, renderApp } from '../../test/api'
import { me, templateCategories, templates } from '../../test/fixtures'

const custom: Template = {
  name: 'custom',
  displayName: 'Custom command',
  description: 'Any command that serves HTTP',
  category: 'web',
  kind: 'custom',
  basePath: 'Prefix',
  appLogin: 'Enabled',
  fileExtensions: null,
  maxSharing: 'Private',
  apiAccessDefault: 'None',
  defaultIdleStopMinutes: 60,
  maxApiAccess: 'ReadWrite',
  userCommand: 'Required',
  userImage: false,
  fromRepo: 'None',
  port: 8080,
}

function withTemplates() {
  mockApi({
    'GET /api/me': { body: me },
    'GET /api/workspaces': { body: { items: [] } },
    'GET /api/templates': { body: { items: [custom, ...templates], categories: templateCategories } },
  })
}

describe('Templates', () => {
  it('is reachable from the sidebar', async () => {
    withTemplates()
    renderApp('/')
    const nav = screen.getByRole('navigation', { name: 'Main' })
    await userEvent.click(within(nav).getByRole('link', { name: 'Templates' }))
    expect(await screen.findByRole('heading', { level: 1, name: 'Templates' })).toBeTruthy()
  })

  it('lists every template under its category heading, each card with one line', async () => {
    withTemplates()
    renderApp('/templates')
    await screen.findByRole('heading', { level: 2, name: 'Web apps and APIs' })
    expect(screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent)).toEqual([
      'Notebooks and IDEs',
      'Dashboards and visualization',
      'Web apps and APIs',
    ])
    const web = screen.getByRole('group', { name: 'Web apps and APIs' })
    const link = within(web).getByRole('link', { name: 'New Custom command app' })
    expect(within(link).getByText('Any command that serves HTTP')).toBeTruthy()
    const streamlit = within(screen.getByRole('group', { name: 'Dashboards and visualization' })).getByRole('link')
    expect(streamlit.textContent).toBe('StreamlitPython data apps')
  })

  it('starts a new app from the chosen template', async () => {
    withTemplates()
    renderApp('/templates')
    await userEvent.click(await screen.findByRole('link', { name: 'New Custom command app' }))
    expect(await screen.findByRole('button', { name: 'Custom command, change' })).toBeTruthy()
  })
})
