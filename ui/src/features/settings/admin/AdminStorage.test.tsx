import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mockApi, renderApp } from '../../../test/api'
import { me } from '../../../test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

const admin = { ...me, roles: ['platform-admin'] }

function row(fact: string, over: Record<string, unknown> = {}) {
  return {
    fact,
    provenance: 'known',
    attention: false,
    evidence: [],
    notice: { code: fact, tone: 'info', title: `Title of ${fact}`, text: `Text of ${fact}`, action: `Action of ${fact}`, detail: `Detail of ${fact}` },
    ...over,
  }
}

const failed = row('probe', {
  attention: true,
  provenance: 'verified',
  evidence: ['step check failed on node-1'],
  notice: {
    code: 'AdminProbeFailed',
    tone: 'err',
    title: 'The shared storage test failed',
    text: 'This storage is not used for home folders until the test passes.',
    action: 'Fix the cause on the cluster, then run the test again.',
    detail: 'StorageClass filestore failed at the check step.',
  },
})
const facts = ['home', 'folders', 'snapshots', 'size'].map((f) => row(f))
const summary = "Files are stored on this machine's disk. Nothing needs attention."

function api(body: object) {
  return mockApi({ 'GET /api/me': { body: admin }, 'GET /api/admin/storage': { body } })
}

describe('Storage settings', () => {
  it('is one line and nothing else when nothing needs attention', async () => {
    api({ summary, rows: facts })
    renderApp('/settings/storage')
    expect(await screen.findByText(summary)).toBeTruthy()
    expect(screen.queryByText('Title of home')).toBeNull()
    expect(document.querySelector('.banner, .card')).toBeNull()
    expect(screen.queryByRole('button', { name: /Details/ })).toBeNull()
  })

  it('shows a card only for what needs an admin, with technical names folded under Details', async () => {
    api({ summary: 'Files are on shared storage.', rows: [failed, ...facts] })
    renderApp('/settings/storage')
    const card = (await screen.findByText('The shared storage test failed')).closest('.banner') as HTMLElement
    expect(within(card).getByText('Fix the cause on the cluster, then run the test again.')).toBeTruthy()
    expect(screen.queryByText('Title of home')).toBeNull()
    expect(within(card).queryByText('verified')).toBeNull()
    expect(within(card).queryByText(/StorageClass/)).toBeNull()
    await userEvent.click(within(card).getByRole('button', { name: 'Details' }))
    expect(within(card).getByText(/StorageClass filestore failed at the check step\./)).toBeTruthy()
    expect(within(card).getByText(/step check failed on node-1/)).toBeTruthy()
  })

  it('offers no way to change storage', async () => {
    api({ summary, rows: [failed] })
    renderApp('/settings/storage')
    await screen.findByText('The shared storage test failed')
    expect(screen.queryByRole('button', { name: /Verify|Schedule/ })).toBeNull()
    expect(document.querySelector('input')).toBeNull()
  })

  it('is a section of Settings, and the old address leads there', async () => {
    api({ summary, rows: facts })
    renderApp('/admin/storage')
    expect(await screen.findByText(summary)).toBeTruthy()
    expect(screen.getByRole('heading', { level: 1, name: 'Settings' })).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Storage' }).getAttribute('aria-current')).toBe('page')
  })

  it('says it is not here for someone who is not an admin', async () => {
    mockApi({ 'GET /api/me': { body: me }, 'GET /api/admin/storage': { status: 404, body: { title: 'Not found', status: 404 } } })
    renderApp('/settings/storage')
    expect(await screen.findByText('Not found')).toBeTruthy()
  })
})
