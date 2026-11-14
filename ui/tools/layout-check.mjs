// Layout check: opens every page of the mock UI in headless Chrome, in both
// app views, both themes and at desktop and phone widths, and fails when:
//
//   1. two buttons in the same row sit closer than 8px (buttons joined inside
//      one track, such as segmented switches, and tabs are exempt, and so is a
//      button of a sticky bar against content scrolled under the bar);
//   2. a button sits closer than 8px to a bordered box above, below or beside
//      it (a box it is not inside);
//   3. text inside a bordered box comes closer than 8px to the box's border
//      (small chips such as pills and keys, and form controls, are exempt);
//   1b. two buttons stacked one above the other sit closer than 8px (rows
//      of a menu or of the file picker's list are exempt);
//   6. a button sits closer than 12px under a line of text (a description
//      or hint) right above it;
//   5. a section label (small uppercase text) sits closer than 12px under the
//      element above it;
//   7. template cards differ in height, or a card's one line does not fit;
//   4. a disclosure toggle ("Show more") sits closer than 12px to the list,
//      grid or box above it, or closer than 16px to the bottom border of the card it ends.
//
//   pnpm mock --port 5391 &
//   node tools/layout-check.mjs http://localhost:5391
//
// While a modal dialog is open only the dialog is audited.
//
// Uses the installed Chrome over its DevTools protocol and Node's own
// WebSocket, so it adds no dependencies. Set CHROME to point at another build.
import { spawn } from 'node:child_process'
import { mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const base = process.argv[2] ?? 'http://localhost:5173'
const chrome = process.env.CHROME ?? '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome'

// Clicks the first element matching a selector whose text contains the given text.
const click = (sel, text) =>
  `[...document.querySelectorAll(${JSON.stringify(sel)})].find((e) => e.textContent.includes(${JSON.stringify(text)}))?.click()`

// Chooses an option of the select labelled (or aria-labelled) with the given text, as a user would.
const choose = (label, value) =>
  `(() => { const s = [...document.querySelectorAll('label')].find((l) => l.textContent === ${JSON.stringify(label)})?.control ?? document.querySelector('select[aria-label=${JSON.stringify(label)}]');` +
  ` Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(s, ${JSON.stringify(value)});` +
  ` s.dispatchEvent(new Event('change', { bubbles: true })) })()`
const onSchedule = click('label.choice', 'On a schedule')
// Types into the first element matching a selector, as a user would.
const askText = (sel, text) =>
  `(() => { const t = document.querySelector(${JSON.stringify(sel)}); Object.getOwnPropertyDescriptor(Object.getPrototypeOf(t), 'value').set.call(t, ${JSON.stringify(text)}); t.dispatchEvent(new Event('input', { bubbles: true })) })()`
const openPal = `document.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }))`
const typePal = (text) => askText('[cmdk-input]', text)
const enterPal = `document.querySelector('[cmdk-input]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))`
const openPicker = click('button', 'Choose a file from your home folder')

// Each page, and for some the clicks that open the state to look at.
const PAGES = [
  { path: '/' },
  { path: '/apps' },
  { path: '/apps/plots' },
  { path: '/approvals' },
  { path: '/approvals/a1', name: 'one approval from its link' },
  { path: '/device?code=WDJB-MJHT', name: 'approve an agent login' },
  { path: '/device?code=XXXX-EXPD', name: 'expired agent login' },
  { path: '/device' },
  { path: '/apps/orders-api' },
  { path: '/apps/churn' },
  { path: '/apps/big-model', name: 'app too big, sizes open', act: [click('.failure button', 'Change size')] },
  { path: '/templates' },
  { path: '/apps/site', name: 'app from Git with previews on' },
  { path: '/apps/site', name: 'previews settings open', act: [click('button', 'Limit, pull requests from forks and webhook')] },
  { path: '/apps/site', name: 'previews webhook shown once', act: [click('button', 'Limit, pull requests from forks and webhook'), click('button', 'Set up webhook')] },
  { path: '/apps/private-docs', name: 'webhook already set up, replace warning', act: [click('button', 'Limit, pull requests from forks and webhook')] },
  { path: '/apps/broken-docs', name: 'previews on with the catalog title and next step', act: [click('button', 'Limit, pull requests from forks and webhook')] },
  { path: '/apps/site', name: 'token field of an app without one', act: [click('button', 'Add a token for a private repository')] },
  { path: '/apps/private-docs', name: 'private repo with previews off, token options open', act: [click('button', 'Change or remove the access token')] },
  { path: '/apps/broken-docs', name: 'clone failure and a failed look at pull requests' },
  { path: '/apps/orders-node', name: 'back-end from a repository on another host' },
  { path: '/apps/page', name: 'static page from a home file' },
  { path: '/apps/site--pr-12', name: 'preview of a pull request' },
  { path: '/apps/new' },
  { path: '/apps/new', name: 'new app sizes', act: [click('label.choice', 'Streamlit')] },
  { path: '/apps/new?template=streamlit', name: 'new app, Git choice', act: [click('button[role=radio]', 'Git repository')] },
  { path: '/apps/new?template=streamlit', name: 'new app, Git options open', act: [click('button[role=radio]', 'Git repository'), click('button', 'Folder and access token')] },
  { path: '/apps/new?template=static-site', name: 'new static site, Git options open', act: [click('button[role=radio]', 'Git repository'), click('button', 'Folder, access token and build options')] },
  { path: '/apps/new?template=static-site', name: 'new static site from a home file' },
  { path: '/apps/new?template=backend-node', name: 'new Node back-end from the home folder' },
  { path: '/apps/new?template=backend-go', name: 'new Go back-end from Git', act: [click('button[role=radio]', 'Git repository')] },
  { path: '/apps/new?template=claude-code', name: 'new app, a card of the last category chosen' },
  {
    path: '/apps/new?template=streamlit',
    name: 'new app from GitHub with previews and the webhook shown',
    act: [
      click('button[role=radio]', 'Git repository'),
      `(() => { const i = [...document.querySelectorAll('label')].find((l) => l.textContent === 'Repository address').control; Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(i, 'https://github.com/example-org/site'); i.dispatchEvent(new Event('input', { bubbles: true })) })()`,
      click('button[role=switch]', 'Preview open pull requests'),
      click('button', 'Limit, pull requests from forks and webhook'),
      click('button', 'Set up webhook'),
    ],
  },
  { path: '/apps/team-blog', name: 'failed build with its output and build log' },
  { path: '/apps/new', name: 'new app custom amount', act: [click('label.choice', 'Streamlit'), click('label.choice', 'Custom')] },
  { path: '/apps/tuned', name: 'app at a custom amount, sizes open', act: [click('button', 'Change size')] },
  { path: '/apps/new', name: 'new app advanced, stop when idle', act: [click('label.choice', 'Streamlit'), click('button.more-toggle', 'Advanced')] },
  { path: '/apps/new', name: 'new app advanced, stop when idle off', act: [click('label.choice', 'Streamlit'), click('button.more-toggle', 'Advanced'), click('button[role=switch]', 'Stop when idle')] },
  { path: '/apps/new', name: 'new app advanced with API access and stop when idle', act: [click('label.choice', 'Claude Code'), click('button.more-toggle', 'Advanced')] },
  { path: '/apps/plots', name: 'app with stop when idle open', act: [click('button', 'Change when it stops')] },
  { path: '/apps/notebook', name: 'app stopped for being idle' },
  { path: '/jobs' },
  { path: '/jobs/weekly-report', name: 'job with its run log open', act: [click('button', 'Log')] },
  { path: '/jobs/weekly-report', name: 'job with a saved run log open', act: [`document.querySelectorAll('button.log-toggle')[1]?.click()`] },
  { path: '/jobs/print-the-time' },
  { path: '/jobs/new' },
  { path: '/jobs/new', name: 'new job on a schedule', act: [onSchedule] },
  { path: '/jobs/new', name: 'new job with variables', act: [click('button', 'Add a variable'), click('button', 'Add a variable'), choose('From', 'secret')] },
  { path: '/jobs/new?secrets=none', name: 'new job, a variable from a secret with no secrets yet', act: [click('button', 'Add a variable'), choose('From', 'secret')] },
  { path: '/jobs/weekly-report', name: 'job variables open', act: [click('button', 'Change variables')] },
  { path: '/jobs/weekly-report', name: 'job sizes open', act: [click('button', 'Change size')] },
  { path: '/jobs/weekly-report', name: 'job custom size open', act: [click('button', 'Change size'), click('label.choice', 'Custom')] },
  { path: '/jobs/new', name: 'new job, custom size', act: [click('label.choice', 'Custom')] },
  { path: '/jobs/new', name: 'schedule every few minutes', act: [onSchedule, choose('Repeats', 'minutes')] },
  { path: '/jobs/new', name: 'schedule weekly', act: [onSchedule, choose('Repeats', 'weekly')] },
  { path: '/jobs/new', name: 'schedule monthly', act: [onSchedule, choose('Repeats', 'monthly'), choose('On day', '31')] },
  { path: '/jobs/new', name: 'schedule custom', act: [onSchedule, choose('Repeats', 'custom')] },
  { path: '/jobs/weekly-report/edit', name: 'edit a weekly job' },
  { path: '/jobs/new', name: 'schedule meeting daylight saving', act: [onSchedule, choose('Hour', '1'), choose('Minute', '30')] },
  { path: '/jobs/new?files=stale', name: 'file picker updating a listing it showed at once', act: [openPicker] },
  { path: '/jobs/new?files=starting', name: 'file picker loading', act: [openPicker] },
  { path: '/jobs/new?files=starting', name: 'file picker explaining a slow open', act: [openPicker, 'new Promise((r) => setTimeout(r, 3200))'] },
  { path: '/settings' },
  { path: '/settings/secrets' },
  { path: '/settings/secrets?secrets=none', name: 'no secrets yet' },
  { path: '/settings/secrets', name: 'new secret form', act: [click('button', 'New secret')] },
  { path: '/settings/secrets', name: 'replace a secret value', act: [`document.querySelector('button[aria-label="Replace the value of OPENAI_KEY"]')?.click()`] },
  { path: '/settings/secrets', name: 'edit a secret note', act: [`document.querySelector('button[aria-label="Edit the note of SLACK_WEBHOOK_URL"]')?.click()`] },
  { path: '/settings/secrets', name: 'delete a secret apps use', act: [`document.querySelector('button[aria-label="Delete OPENAI_KEY"]')?.click()`, click('[role=alertdialog] button', 'Delete'), 'new Promise((r) => setTimeout(r, 300))'] },
  { path: '/apps/new', name: 'new app advanced with variables', act: [click('label.choice', 'Streamlit'), click('button.more-toggle', 'Advanced'), click('button', 'Add a variable'), click('button', 'Add a variable'), choose('From', 'secret')] },
  { path: '/apps/new?secrets=none', name: 'new app advanced with no secrets yet', act: [click('label.choice', 'Streamlit'), click('button.more-toggle', 'Advanced')] },
  { path: '/apps/plots', name: 'app variables open', act: [click('button', 'Change variables')] },
  { path: '/apps/new?template=streamlit&secrets=none', name: 'new app, Git options open with no secrets for a token', act: [click('label.choice', 'Git repository'), click('button', 'Folder and access token')] },
  { path: '/apps/site?secrets=none', name: 'token field with no secrets yet', act: [click('button', 'Add a token for a private repository')] },
  { path: '/settings/apps' },
  { path: '/settings/apps', name: 'admin stop confirmation', act: [click('button', 'Stop')] },
  { path: '/settings/usage' },
  { path: '/settings/usage', name: 'usage by group', act: [click('.seg button', 'By group')] },
  { path: '/settings/backups' },
  { path: '/settings/backups?backups=none', name: 'backups not set up' },
  { path: '/settings/backups', name: 'restore preview', act: [click('button', 'Preview restore'), 'new Promise((r) => setTimeout(r, 300))'] },
  { path: '/settings/backups', name: 'restore preview with the apps that come back', act: [click('button', 'Preview restore'), 'new Promise((r) => setTimeout(r, 300))', click('button.more-toggle', 'come back')] },
  { path: '/settings/backups', name: 'home folders not backed up listed', act: [click('button.more-toggle', 'not backed up')] },
  { path: '/settings/backups?backups=bucket', name: 'full backups to a bucket, the newest missed a home' },
  { path: '/settings/backups?backups=bucket', name: 'each home and folder shown', act: [click('button.more-toggle', 'Show each home folder'), 'new Promise((r) => setTimeout(r, 300))', click('button.more-toggle', 'Show details')] },
  { path: '/settings/backups?backups=bucket', name: 'restore one person beside their files', act: [choose('Whose files', 'carol-9c0d1e2f'), 'new Promise((r) => setTimeout(r, 300))', click('label.choice', 'Put beside current files')] },
  { path: '/settings/backups?backups=setup', name: 'bucket without a backup job' },
  { path: '/apps/gpu-run', name: 'app stopped by an admin' },
  { path: '/apps/restored', name: 'restored app waiting' },
  { path: '/apps', name: 'import open', act: [click('button', 'Import')] },
  {
    path: '/apps',
    name: 'import preview',
    act: [
      click('button', 'Import'),
      `(() => { const i = document.querySelector('input[type=file]'); const t = new DataTransfer(); t.items.add(new File(['apiVersion: javo.nebari.dev/v1alpha1'], 'javo-apps.yaml')); i.files = t.files; i.dispatchEvent(new Event('change', { bubbles: true })) })()`,
      'new Promise((r) => setTimeout(r, 300))',
      click('button.more-toggle', 'to add'),
    ],
  },
  { path: '/apps', name: 'palette with nothing typed', act: [`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }))`] },
  { path: '/apps', name: 'palette results', act: [`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }))`, `(() => { const i = document.querySelector('[cmdk-input]'); Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(i, 'sa'); i.dispatchEvent(new Event('input', { bubbles: true })) })()`] },
  { path: '/apps', name: 'palette with no results', act: [`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }))`, `(() => { const i = document.querySelector('[cmdk-input]'); Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(i, 'zzqx'); i.dispatchEvent(new Event('input', { bubbles: true })) })()`] },
  { path: '/apps', name: 'palette actions of an app', act: [`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }))`, `(() => { const i = document.querySelector('[cmdk-input]'); Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(i, 'sales app'); i.dispatchEvent(new Event('input', { bubbles: true })) })()`, `document.querySelector('[cmdk-input]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }))`] },
  { path: '/apps', name: 'palette stop confirmation', act: [`document.dispatchEvent(new KeyboardEvent('keydown', { key: 'k', metaKey: true, bubbles: true }))`, `(() => { const i = document.querySelector('[cmdk-input]'); Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(i, 'sales app'); i.dispatchEvent(new Event('input', { bubbles: true })) })()`, `document.querySelector('[cmdk-input]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Tab', bubbles: true }))`, `(() => { const i = document.querySelector('[cmdk-input]'); Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(i, 'stop'); i.dispatchEvent(new Event('input', { bubbles: true })) })()`, `document.querySelector('[cmdk-input]').dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))`] },
  { path: '/apps/churn', name: 'failure with its fix in review', act: [click('.failure button', 'Restart Churn dashboard')] },
  { path: '/apps/churn', name: 'failure with the AI explanation', act: [click('button', 'Explain more')] },
  { path: '/apps/churn', name: 'failure with what will be sent', act: [click('button', 'Explain more'), click('button.more-toggle', 'What will be sent')] },
  { path: '/apps/churn?ai=off', name: 'failure with AI off' },
  { path: '/apps/model', name: 'unknown failure, AI asked at once' },
  { path: '/plans/p1', name: 'plan from an agent' },
  { path: '/apps/new', name: 'new app described', act: [askText('textarea', 'A Streamlit app from sales/app.py called Sales'), click('button', 'Fill the form')] },
  { path: '/apps', name: 'palette ask AI', act: [openPal, typePal('stop my idle apps'), enterPal] },
  { path: '/apps', name: 'palette ask AI for a new app', act: [openPal, typePal('create a streamlit dashboard'), enterPal] },
  { path: '/apps?admin=1', name: 'palette cluster question', act: [openPal, typePal('why are apps stuck'), click('[cmdk-item]', 'Ask about this cluster')] },
  { path: '/apps/new?template=streamlit', name: 'file picker from a sample not copied yet', act: [click('button', 'Change')] },
  { path: '/apps/new?template=streamlit&samples=copied', name: 'file picker from a copied sample', act: [click('button', 'Change')] },
  { path: '/apps/new?template=claude-code', name: 'folder picker from a sample not copied yet', act: [click('button', 'Change')] },
  { path: '/apps/new?template=claude-code&samples=copied', name: 'folder picker from a copied sample', act: [click('button', 'Change')] },
  { path: '/?scenario=noshared', name: 'home with storage banners' },
  { path: '/?scenario=noshared', name: 'home with storage banners opened', act: [click('button.more-toggle', 'Show')] },
  { path: '/apps?scenario=noshared', name: 'apps with storage banners' },
  { path: '/apps/new?scenario=noshared', name: 'new app placement banner', act: [click('label.choice', 'Streamlit')] },
  { path: '/apps/new?template=streamlit', name: 'file picker with group folder places', act: [click('button', 'Change'), click('.pk-side button', 'data-science')] },
  { path: '/apps/sales-agent', name: 'locked agent with what it can open' },
  { path: '/apps/sales-agent', name: 'adding a folder a locked agent can open', act: [click('button', 'Add a folder or file'), click('.pk-row', 'data')] },
  { path: '/apps/plots', name: 'app with the folders that are not saved', act: [click('button.more-toggle', 'Folders that are not saved')] },
  { path: '/settings/storage?admin=1', name: 'storage settings with things to fix', act: [click('button.more-toggle', 'Details')] },
  { path: '/settings/storage?admin=1&scenario=quiet', name: 'storage settings, nothing needs attention' },
  { path: '/apps/new?template=claude-code&samples=copied', name: 'folder picker inside the sample with hidden files', act: [click('button', 'Change'), `document.querySelector('button[aria-label="Open claude-code"]').click()`, `document.querySelector('button[aria-label="Show hidden files"]').click()`] },
]
const MIN_GAP = 8
const MIN_PAD = 8
const MORE_ABOVE = 12
const MORE_BELOW = 16
const LABEL_ABOVE = 12
const TEXT_ABOVE = 12

// Runs in the page; returns one line per defect.
const AUDIT = `(() => {
  // An open modal dialog is all the user can reach: the page under it is not audited.
  const scope = document.querySelector('[aria-modal=true]') ?? document.body
  const visible = (e) => { const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0 && getComputedStyle(e).visibility !== 'hidden' }
  const label = (e) => ((e.innerText || e.getAttribute('aria-label') || e.className || e.tagName) + '').trim().replace(/\\s+/g, ' ').slice(0, 30)
  const out = []

  // The bar a button stays put in while the page scrolls (sticky or fixed), or null.
  const bar = (e) => { for (let p = e; p; p = p.parentElement) if (/sticky|fixed/.test(getComputedStyle(p).position)) return p; return null }
  // Content scrolled under a bar meets the bar's buttons in no layout: such pairs are no defect.
  const apart = (a, b) => bar(a) !== bar(b)

  // 1. Buttons side by side.
  const buttons = [...scope.querySelectorAll('button, a.btn, .btn')].filter(visible)
  for (const a of buttons) for (const b of buttons) {
    if (a === b || a.contains(b) || b.contains(a) || apart(a, b)) continue
    const ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect()
    const overlap = Math.min(ra.bottom, rb.bottom) - Math.max(ra.top, rb.top)
    if (overlap <= Math.min(ra.height, rb.height) * 0.5 || rb.left < ra.left) continue
    const gap = rb.left - ra.right
    const track = a.parentElement === b.parentElement && getComputedStyle(a.parentElement).backgroundColor !== 'rgba(0, 0, 0, 0)'
    if (gap >= -0.5 && gap < ${MIN_GAP} && !track && !a.closest('[role=tablist]')) out.push('buttons ' + label(a) + ' | ' + label(b) + ' = ' + gap.toFixed(1) + 'px')
  }
  // 1b. Buttons stacked one above the other.
  for (const a of buttons) for (const b of buttons) {
    if (a === b || a.contains(b) || b.contains(a) || apart(a, b)) continue
    const ra = a.getBoundingClientRect(), rb = b.getBoundingClientRect()
    const overlap = Math.min(ra.right, rb.right) - Math.max(ra.left, rb.left)
    if (overlap <= Math.min(ra.width, rb.width) * 0.5 || rb.top < ra.bottom - 0.5) continue
    const gap = rb.top - ra.bottom
    if (gap < ${MIN_GAP} && !a.closest('[role=tablist], .seg, .menu, .pk-list')) out.push('stacked buttons ' + label(a) + ' over ' + label(b) + ' = ' + gap.toFixed(1) + 'px')
  }

  // A box drawn with a border on all four sides.
  const framed = (e) => {
    const s = getComputedStyle(e)
    return ['Top', 'Right', 'Bottom', 'Left'].every((k) => parseFloat(s['border' + k + 'Width']) >= 1 && s['border' + k + 'Style'] !== 'none' && !/rgba\\(.*, 0\\)$/.test(s['border' + k + 'Color']))
  }
  const control = 'button, .btn, input, select, textarea, [role=tablist], .seg, .kbd, .pill'
  const boxes = [...scope.querySelectorAll('*')].filter((e) => visible(e) && framed(e) && !e.matches(control) && !e.closest(control))

  // Bars that stay put while the page scrolls under them.
  const pinned = (e) => { for (let p = e; p; p = p.parentElement) if (/sticky|fixed/.test(getComputedStyle(p).position)) return true; return false }

  // 2. A button against a box it is not inside.
  for (const b of buttons) {
    if (b.closest('[role=tablist], .seg') || pinned(b)) continue
    const rb = b.getBoundingClientRect()
    for (const x of boxes) {
      if (x.contains(b) || b.contains(x)) continue
      const rx = x.getBoundingClientRect()
      const hOverlap = Math.min(rb.right, rx.right) - Math.max(rb.left, rx.left)
      const vOverlap = Math.min(rb.bottom, rx.bottom) - Math.max(rb.top, rx.top)
      let gap = Infinity
      if (hOverlap > 0) gap = Math.min(gap, ...[rx.top - rb.bottom, rb.top - rx.bottom].filter((g) => g >= -0.5))
      if (vOverlap > 0) gap = Math.min(gap, ...[rx.left - rb.right, rb.left - rx.right].filter((g) => g >= -0.5))
      if (gap < ${MIN_GAP}) out.push('button ' + label(b) + ' against box ' + label(x) + ' = ' + gap.toFixed(1) + 'px')
    }
  }

  // 3. Text against the border of the box it is in.
  for (const x of boxes) {
    const rx = x.getBoundingClientRect()
    if (rx.height < 36) continue
    const s = getComputedStyle(x)
    const inner = { top: rx.top + parseFloat(s.borderTopWidth), bottom: rx.bottom - parseFloat(s.borderBottomWidth), left: rx.left + parseFloat(s.borderLeftWidth), right: rx.right - parseFloat(s.borderRightWidth) }
    const scrollsY = x.scrollHeight > x.clientHeight + 1
    const scrollsX = x.scrollWidth > x.clientWidth + 1
    const walker = document.createTreeWalker(x, NodeFilter.SHOW_TEXT)
    let worst = null
    for (let t = walker.nextNode(); t; t = walker.nextNode()) {
      if (!t.textContent.trim()) continue
      const host = t.parentElement
      if (!host || !visible(host) || host.closest(control) || host.closest('.sr-only')) continue
      // Only text whose nearest box is this one.
      let p = host
      while (p && p !== x && !boxes.includes(p)) p = p.parentElement
      if (p !== x) continue
      const range = document.createRange()
      range.selectNodeContents(t)
      for (const r of range.getClientRects()) {
        if (r.width < 1) continue
        const sides = []
        if (!scrollsX) sides.push(r.left - inner.left, inner.right - r.right)
        if (!scrollsY) sides.push(r.top - inner.top, inner.bottom - r.bottom)
        if (sides.length === 0) continue
        // Text cut off by a scrolling or clipped box is not a padding problem.
        if (sides.some((d) => d < -1)) continue
        const d = Math.min(...sides)
        if (d < ${MIN_PAD} && (!worst || d < worst.d)) worst = { d, text: t.textContent.trim().slice(0, 24) }
      }
    }
    if (worst) out.push('text "' + worst.text + '" against the border of ' + label(x) + ' = ' + worst.d.toFixed(1) + 'px')
  }

  // 4. Disclosure toggles: air above the list, grid or box they follow, and the card's padding below.
  const block = (e) => /^(UL|OL|DL|TABLE)$/.test(e.tagName) || /grid|flex/.test(getComputedStyle(e).display) && e.childElementCount > 1 || boxes.includes(e)
  for (const t of [...scope.querySelectorAll('.more-toggle')].filter(visible)) {
    const more = t.closest('.more')
    // A toggle outside .more (the palette's AskAi) has no list to measure against.
    if (!more) continue
    const rt = t.getBoundingClientRect()
    let prev = more.previousElementSibling
    while (prev && !visible(prev)) prev = prev.previousElementSibling
    if (prev && block(prev)) {
      const gap = rt.top - prev.getBoundingClientRect().bottom
      if (gap < ${MORE_ABOVE}) out.push('toggle ' + label(t) + ' under ' + label(prev) + ' = ' + gap.toFixed(1) + 'px')
    }
    const card = more.closest('.card')
    const body = more.querySelector('.more-body')
    const open = body && body.childElementCount > 0
    if (card && !open) {
      // The toggle ends the card when nothing visible follows it inside the card.
      const rc = card.getBoundingClientRect()
      const after = [...card.querySelectorAll('*')].some((e) => visible(e) && !more.contains(e) && e.getBoundingClientRect().top >= rt.bottom - 0.5 && !e.contains(more))
      const gap = rc.bottom - parseFloat(getComputedStyle(card).borderBottomWidth) - rt.bottom
      if (!after && gap < ${MORE_BELOW}) out.push('toggle ' + label(t) + ' above the bottom of its card = ' + gap.toFixed(1) + 'px')
    }
  }
  // 7. Template cards: one size, and their one line fits.
  const tplCards = [...scope.querySelectorAll('.choice.tpl')].filter(visible)
  const heights = new Set(tplCards.map((c) => Math.round(c.getBoundingClientRect().height)))
  if (heights.size > 1) out.push('template cards of ' + [...heights].join(', ') + 'px')
  for (const c of tplCards) for (const l of c.querySelectorAll('h3, .one-line')) if (l.scrollWidth > l.clientWidth + 0.5) out.push('template card text cut: ' + label(l))

  // 6. A button straight under a line of text (a description or hint) gets 12px.
  const textBlock = (e) => !e.matches('button, a, label, .btn, .field-label, input, select, textarea') && !/inline/.test(getComputedStyle(e).display) && [...e.childNodes].some((n) => n.nodeType === 3 && n.textContent.trim()) && !e.querySelector('button, input, select, textarea, .btn')
  for (const b of buttons) {
    let prev = b.previousElementSibling
    while (prev && !visible(prev)) prev = prev.previousElementSibling
    if (!prev || !textBlock(prev)) continue
    const rp = prev.getBoundingClientRect(), rb = b.getBoundingClientRect()
    const gap = rb.top - rp.bottom
    if (gap > -0.5 && gap < ${TEXT_ABOVE}) out.push('button ' + label(b) + ' under text ' + label(prev) + ' = ' + gap.toFixed(1) + 'px')
  }

  // 5. Section labels (small uppercase) get air under whatever is above them.
  for (const e of [...scope.querySelectorAll('*')].filter(visible)) {
    const st = getComputedStyle(e)
    // Shapes inside an icon inherit the label's style but are not text.
    if (st.textTransform !== 'uppercase' || !(parseFloat(st.letterSpacing) > 0) || e.closest('thead, nav, .side, .seg, svg')) continue
    if ([...e.children].some((c) => getComputedStyle(c).textTransform === 'uppercase')) continue
    let prev = e.previousElementSibling
    while (prev && !visible(prev)) prev = prev.previousElementSibling
    if (!prev) continue
    const gap = e.getBoundingClientRect().top - prev.getBoundingClientRect().bottom
    if (gap > -0.5 && gap < ${LABEL_ABOVE}) out.push('label ' + label(e) + ' under ' + label(prev) + ' = ' + gap.toFixed(1) + 'px')
  }
  return out
})()`

const profile = mkdtempSync(join(tmpdir(), 'javo-layout-'))
const proc = spawn(chrome, ['--headless=new', '--remote-debugging-port=0', `--user-data-dir=${profile}`, '--window-size=1440,900', 'about:blank'], {
  stdio: ['ignore', 'ignore', 'pipe'],
})
const wsURL = await new Promise((resolve, reject) => {
  let err = ''
  proc.stderr.on('data', (d) => {
    err += d
    const m = err.match(/DevTools listening on (ws:\S+)/)
    if (m) resolve(m[1])
  })
  proc.on('exit', () => reject(new Error(`Chrome exited before it was ready:\n${err}`)))
})

// Minimal DevTools client: one browser connection, flat sessions per target.
const ws = new WebSocket(wsURL)
await new Promise((r) => (ws.onopen = r))
let id = 0
const pending = new Map()
ws.onmessage = (m) => {
  const msg = JSON.parse(m.data)
  if (msg.id && pending.has(msg.id)) {
    const { resolve, reject } = pending.get(msg.id)
    pending.delete(msg.id)
    msg.error ? reject(new Error(msg.error.message)) : resolve(msg.result)
  }
}
const send = (method, params = {}, sessionId) =>
  new Promise((resolve, reject) => {
    pending.set(++id, { resolve, reject })
    ws.send(JSON.stringify({ id, method, params, sessionId }))
  })
const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const { targetId } = await send('Target.createTarget', { url: 'about:blank' })
const { sessionId } = await send('Target.attachToTarget', { targetId, flatten: true })
const evaluate = async (expression) =>
  (await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }, sessionId)).result.value

const WIDTHS = [
  [1440, 'desktop'],
  [390, 'phone'],
]
const failures = []
try {
  for (const [width, device] of WIDTHS) {
    await send('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: width < 500 }, sessionId)
    for (const theme of ['light', 'dark']) {
      await send('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-color-scheme', value: theme }] }, sessionId)
      for (const view of ['cards', 'list']) {
        await send('Page.navigate', { url: base + '/' }, sessionId)
        await sleep(800)
        await evaluate(`localStorage.setItem('javo.appsView', '${view}')`)
        for (const page of PAGES) {
          // Only the apps pages differ between the two views.
          if (view === 'list' && !/^\/(apps)?$/.test(page.path)) continue
          await send('Page.navigate', { url: base + page.path }, sessionId)
          await sleep(1200)
          for (const step of page.act ?? []) {
            await evaluate(step)
            await sleep(600)
          }
          const where = `${device} ${theme} ${view} ${page.name ?? page.path}`
          for (const f of await evaluate(AUDIT)) failures.push(`${where}: ${f}`)
        }
      }
    }
  }
} finally {
  ws.close()
  const exited = new Promise((r) => proc.once('exit', r))
  proc.kill()
  await exited
  rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 })
}

if (failures.length) {
  console.error(`Layout defects:\n` + [...new Set(failures)].map((f) => '  ' + f).join('\n'))
  process.exit(1)
}
console.log(`layout check: ${PAGES.length} pages x 2 widths x 2 themes, no defects`)
