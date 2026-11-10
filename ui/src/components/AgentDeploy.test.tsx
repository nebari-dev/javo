import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../test/api'
import { me, templates } from '../test/fixtures'
import { AgentDeploy } from './AgentDeploy'

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const LINE = () => `Deploy the app in this folder to Javo. Follow ${window.location.origin}/llms.txt`

describe('AgentDeploy', () => {
  it('shows the line to paste, built from the address the person is on', () => {
    const { container } = render(<AgentDeploy />)
    expect(screen.getByText('Deploy from your computer')).toBeTruthy()
    expect(screen.getByText("Paste this into your coding agent in the app's folder.")).toBeTruthy()
    expect(container.querySelector('code')?.textContent).toBe(LINE())
  })

  it('copies exactly that line and says Copied for a moment', async () => {
    const user = userEvent.setup()
    render(<AgentDeploy />)
    await user.click(screen.getByRole('button', { name: 'Copy' }))
    expect(await navigator.clipboard.readText()).toBe(LINE())
    expect(screen.getByRole('button', { name: 'Copied' })).toBeTruthy()
    await vi.waitFor(() => expect(screen.getByRole('button', { name: 'Copy' })).toBeTruthy(), { timeout: 3000 })
  })
})

describe('where the panel appears', () => {
  it('on the new app page', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/templates': { body: { items: templates } }, 'GET /api/workspaces': { body: { items: [] } }, 'GET /api/sizes': { body: { items: [] } } })
    renderApp('/apps/new')
    expect(await screen.findByText('Deploy from your computer')).toBeTruthy()
  })

  it('above the API tokens list', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/info': { body: { apiDocs: false, tokenMaxDays: 0 } }, 'GET /api/tokens': { body: [] } })
    renderApp('/settings')
    expect(await screen.findByText('Deploy from your computer')).toBeTruthy()
  })
})
