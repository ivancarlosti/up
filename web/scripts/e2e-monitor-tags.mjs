#!/usr/bin/env node
/**
 * End to end check of the admin monitor tags page.
 *
 * Tags have no ids (they live as a comma separated string on each monitor), so
 * the page has no create/delete and only one write: a rename, which is a bulk
 * edit over every monitor carrying the tag. This script drives the real UI: it
 * seeds three monitors with tags through the API, checks the counts and the
 * Monitors column of the table, the remembered sort choice, a rename through the
 * two-step dialog (a dry run preview and then the apply) and the merge warning
 * that appears when the new name is already in use. Everything it creates is
 * removed at the end.
 *
 * Usage:
 *   npm run build && npm run e2e:monitor-tags -- --url http://localhost:3000
 *
 * Requirements: a running instance with the built bundle, Chrome/Chromium and a
 * writable database (the fixtures are created and deleted through the API).
 */
import { readFileSync } from 'node:fs'
import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** The column labels and the messages come from the default locale (fresh profile). */
const readLocale = (name) => JSON.parse(readFileSync(new URL(`../src/locales/${name}.json`, import.meta.url), 'utf8'))
const locale = readLocale('en-US')
const LOCALES = ['en-US', 'pt-BR', 'es-MX', 'fr-FR', 'ar-SA', 'hi-IN', 'zh-CN'].map(readLocale)

function parseArgs(argv) {
  let url = process.env.SMOKE_BASE_URL ?? 'http://localhost:3000'
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--url') url = argv[++i] ?? url
    else {
      console.error(`unknown option: ${argv[i]}`)
      process.exit(2)
    }
  }
  return url
}
const url = parseArgs(process.argv.slice(2))

const failures = []
function check(label, ok, extra = '') {
  console.log(`  ${ok ? 'ok' : 'x '} ${label}${extra ? ` (${extra})` : ''}`)
  if (!ok) failures.push(label)
}

/** apiCall seeds and removes the fixtures (the instance runs with AUTH_METHOD=none). */
async function apiCall(path, method = 'GET', body) {
  const response = await fetch(`${url}${path}`, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  })
  if (!response.ok) throw new Error(`${method} ${path} -> ${response.status} ${await response.text()}`)
  return response.status === 204 ? null : response.json()
}

const stamp = Date.now()
const prefix = `zz-tag-${stamp}`
const tag = { prod: `${prefix}-prod`, web: `${prefix}-web`, ops: `${prefix}-ops` }
const renameTo = `${prefix}-ops2`
const created = { monitors: [] }

/** SNAPSHOT reads one table and the page text back out of the DOM. */
const SNAPSHOT = `(() => ({
  headers: [...document.querySelectorAll('table thead th')].map((th) => ({
    label: th.innerText.trim(),
    sort: th.getAttribute('aria-sort'),
  })),
  rows: [...document.querySelectorAll('table tbody tr')].map((tr) => {
    const cells = [...tr.querySelectorAll('td')]
    return {
      name: (cells[0]?.innerText ?? '').split('\\n')[0].trim(),
      count: (cells[1]?.innerText ?? '').trim(),
    }
  }),
  body: document.body.innerText,
}))()`

const chrome = findChrome()
const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })

const readTable = () => page.evaluate(SNAPSHOT)
const namesOf = (table) => table.rows.map((row) => row.name)

/** setFilter types into the tag filter, found by the placeholder the view sets. */
const setFilter = (value) =>
  page.evaluate(`(() => {
    const input = [...document.querySelectorAll('input')].find(
      (field) => field.placeholder === ${JSON.stringify(locale.tags.filterPlaceholder)},
    )
    if (!input) return false
    input.value = ${JSON.stringify(value)}
    input.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  })()`)

/** storedSort reads the remembered sort of the table out of localStorage. */
const storedSort = () => page.evaluate(`localStorage.getItem('up.admin.monitor-tags.sort')`)

/** clickHeader clicks the sort button of a column, by the index of the header cell. */
const clickHeader = (index) =>
  page.evaluate(`(() => {
    const cell = [...document.querySelectorAll('table thead th')][${index}]
    const button = cell?.querySelector('button')
    if (!button) return false
    button.click()
    return true
  })()`)

/**
 * clickInRow clicks the icon button of the row whose tag is the given name. The
 * rename action is icon only, so its accessible name (title or aria-label) is
 * what identifies it.
 */
const clickInRow = (name) =>
  page.evaluate(`(() => {
    const label = ${JSON.stringify(locale.tags.rename)}
    const row = [...document.querySelectorAll('table tbody tr')].find(
      (tr) => ((tr.querySelector('td')?.innerText ?? '').split('\\n')[0] ?? '').trim() === ${JSON.stringify(name)},
    )
    const button = row && [...row.querySelectorAll('button')].find(
      (b) => (b.getAttribute('title') ?? '') === label || (b.getAttribute('aria-label') ?? '') === label,
    )
    if (!button) return false
    button.click()
    return true
  })()`)

/** clickDialogButton clicks the button of the open dialog whose text matches. */
const clickDialogButton = (text) =>
  page.evaluate(`(() => {
    const dialog = document.querySelector('[role="dialog"]')
    const button = dialog && [...dialog.querySelectorAll('button')].find((b) => (b.textContent ?? '').trim() === ${JSON.stringify(text)})
    if (!button) return false
    button.click()
    return true
  })()`)

/** setNewName types into the rename input (the view gives it a stable id). */
const setNewName = (value) =>
  page.evaluate(`(() => {
    const el = document.querySelector('#tag-new-name')
    if (!el) return false
    el.value = ${JSON.stringify(value)}
    el.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  })()`)

/** tagsOf reads a monitor's tags back through the API. */
async function tagsOf(id) {
  const list = await apiCall('/api/monitors?decorate=false')
  const monitor = list.find((row) => row.id === id)
  return monitor ? monitor.tags.split(',').filter(Boolean) : []
}

try {
  console.log(`> ${url} with ${chrome}`)

  // The profile is fresh (browser.mjs creates one per run), but clearing it is
  // cheap insurance: a warm one would carry the locale and the sort of an
  // earlier run and turn the "default column" checks red.
  await page.goto(url, { settle: 800 })
  await page.evaluate('localStorage.clear()')

  // --- fixtures: three monitors whose tags give 3 / 2 / 1 -------------------
  for (const [key, monitorTags] of [
    ['a', [tag.prod, tag.web, tag.ops]],
    ['b', [tag.prod, tag.web]],
    ['c', [tag.prod]],
  ]) {
    const monitor = await apiCall('/api/monitors', 'POST', {
      name: `${prefix} monitor ${key}`,
      type: 'http',
      active: false,
      interval_seconds: 3600,
      timeout_seconds: 10,
      tags: monitorTags.join(','),
      config: { url: `http://127.0.0.1:8099/${prefix}-${key}`, method: 'GET' },
    })
    created.monitors.push(monitor.id)
  }
  check(
    'the three tagged monitors were created',
    created.monitors.length === 3 && created.monitors.every((id) => id > 0),
    created.monitors.join(', '),
  )
  const [m1] = created.monitors

  // The counter is compared against the total the API reports, so the check
  // holds whatever else the database already contains.
  const allTags = await apiCall('/api/monitors/tags')

  // --- the table ------------------------------------------------------------
  await page.goto(`${url}/admin/monitor-tags`, { settle: 1600 })
  check('sidebar links the tags page', await page.evaluate(`!!document.querySelector('a[href="/admin/monitor-tags"]')`))
  check('the filter input is there', await setFilter(prefix))
  await sleep(400)
  const table = await readTable()
  check('the filter keeps the three tags', table.rows.length === 3, namesOf(table).join(', '))
  const byName = Object.fromEntries(table.rows.map((row) => [row.name, row.count]))
  check(
    'the counts match the monitors',
    byName[tag.prod] === '3' && byName[tag.web] === '2' && byName[tag.ops] === '1',
    JSON.stringify(byName),
  )
  check(
    'the filter reports the shown count',
    LOCALES.some((messages) =>
      table.body.includes(messages.common.shownOfTotal.replace('{shown}', '3').replace('{total}', String(allTags.length))),
    ),
    `3 of ${allTags.length}`,
  )
  check(
    'the default column is the tag name, ascending',
    table.headers[0]?.sort === 'ascending' && table.headers[0]?.label === locale.common.tags,
    `${table.headers[0]?.label} ${table.headers[0]?.sort}`,
  )
  check('the Monitors column is there', table.headers[1]?.label === locale.tags.monitors, table.headers[1]?.label)

  // --- the Monitors column orders by count ---------------------------------
  check('clicking the Monitors header works', await clickHeader(1))
  await sleep(400)
  const ascending = await readTable()
  check('the column announces ascending', ascending.headers[1]?.sort === 'ascending', String(ascending.headers[1]?.sort))
  check(
    'the rows follow ascending',
    namesOf(ascending).join('|') === [tag.ops, tag.web, tag.prod].join('|'),
    namesOf(ascending).join(', '),
  )

  check('clicking it again works', await clickHeader(1))
  await sleep(400)
  const descending = await readTable()
  check('the column announces descending', descending.headers[1]?.sort === 'descending', String(descending.headers[1]?.sort))
  check(
    'the rows follow descending',
    namesOf(descending).join('|') === [tag.prod, tag.web, tag.ops].join('|'),
    namesOf(descending).join(', '),
  )

  const stored = JSON.parse((await storedSort()) ?? 'null')
  check('the choice is remembered', stored?.key === 'monitors' && stored?.direction === 'desc', JSON.stringify(stored))

  await page.goto(`${url}/admin/monitor-tags`, { settle: 1600 })
  await setFilter(prefix)
  await sleep(400)
  const reloaded = await readTable()
  check(
    'the choice survives a reload',
    reloaded.headers[1]?.sort === 'descending' && namesOf(reloaded).join('|') === [tag.prod, tag.web, tag.ops].join('|'),
    namesOf(reloaded).join(', '),
  )

  // --- rename: the dry run preview and then the apply ----------------------
  check('the rename action opens the dialog', await clickInRow(tag.ops))
  await sleep(400)
  check('the dialog is titled', (await readTable()).body.includes(locale.tags.renameTitle))
  check('the new name starts as the current tag', await setNewName(renameTo))
  check('the preview runs first', await clickDialogButton(locale.tags.preview))
  await sleep(700)
  const wanted = locale.tags.willUpdate.replace('{updated}', '1').replace('{unchanged}', '0')
  check('the preview reports the dry run', (await readTable()).body.includes(wanted), wanted)
  check('the apply renames the tag', await clickDialogButton(locale.tags.apply))
  await sleep(1600)
  const renamedTags = await tagsOf(m1)
  check('the renamed tag is on the monitor', renamedTags.includes(renameTo), renamedTags.join(', '))
  check('the old tag left the monitor', !renamedTags.includes(tag.ops), renamedTags.join(', '))

  await setFilter(prefix)
  await sleep(400)
  const afterRename = await readTable()
  check(
    'the table shows the new tag and not the old one',
    namesOf(afterRename).includes(renameTo) && !namesOf(afterRename).includes(tag.ops),
    namesOf(afterRename).join(', '),
  )

  // --- the merge warning when the new name is already in use ---------------
  check('the rename action opens again', await clickInRow(tag.web))
  await sleep(400)
  check('typing an existing tag', await setNewName(tag.prod))
  await sleep(300)
  const merge = locale.tags.mergeWarning.replace('{tag}', tag.prod)
  check('the merge is warned about', (await readTable()).body.includes(merge), merge)
  check('the dialog cancels', await clickDialogButton(locale.common.cancel))

  check('no console errors', page.consoleMessages.length === 0, page.consoleMessages.slice(0, 2).join(' | '))
  check('no page exceptions', page.exceptions.length === 0, page.exceptions.slice(0, 2).join(' | '))
  await page.screenshot('/tmp/up-e2e-monitor-tags.png')
} finally {
  // The removal lives here, so a check that throws (not just fails) still leaves
  // no fixture behind: 404 answers from an earlier attempt are ignored.
  const codes = []
  for (const id of created.monitors) {
    const response = await fetch(`${url}/api/monitors/${id}`, { method: 'DELETE' }).catch(() => ({ status: 0 }))
    codes.push(response.status)
  }
  check('cleanup removed every fixture', codes.every((code) => code === 204 || code === 404), JSON.stringify(codes))
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x monitor tags check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok the monitor tags page lists, sorts and renames the tags')

