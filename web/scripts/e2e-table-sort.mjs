#!/usr/bin/env node
/**
 * End to end check of the header widget the three admin tables share: the
 * monitor groups, the monitor templates and the status pages.
 *
 * None of it is visible to `npm run typecheck`: that `loadTableSort` reads back
 * what `toggleTableSort` wrote, that the header button reorders the rows and that
 * the filter hides them only exists in the DOM. The script therefore drives the
 * real UI - three fixtures per table created through the API, one click per
 * direction, a reload for the remembered choice, a filter term for the *shown of
 * total* counter and a term that matches nothing for the empty state.
 *
 * Usage:
 *   npm run build && npm run e2e:table-sort -- --url http://localhost:3000
 *
 * Requirements: a running instance with the built bundle, Chrome/Chromium and a
 * writable database (the fixtures are created and deleted through the API).
 */
import { readFileSync } from 'node:fs'
import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/**
 * The column labels come from the default locale (the browser profile starts
 * clean), while the two messages behind the filter are matched against every
 * locale, which is what keeps the script from breaking on a translation.
 */
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
// The term has to be a substring of every fixture name, or the filter would hide
// the rows and every later step would look at an empty table.
const term = `zz sort ${stamp}`
const fixtureNames = ['a', 'b', 'c'].map((key) => `${term} ${key}`)
const created = { monitors: [], groups: [], templates: [], pages: [] }

/** SNAPSHOT is the expression that reads one table back out of the DOM. */
const SNAPSHOT = `(() => ({
  headers: [...document.querySelectorAll('table thead th')].map((th) => ({
    label: th.innerText.trim(),
    sort: th.getAttribute('aria-sort'),
  })),
  rows: [...document.querySelectorAll('table tbody tr')].map((tr) => {
    const cells = [...tr.querySelectorAll('td')]
    return {
      name: (cells[0]?.innerText ?? '').split('\\n')[0].trim(),
      cells: cells.map((cell) => cell.innerText.replace(/\\s+/g, ' ').trim()),
    }
  }),
  body: document.body.innerText.replace(/\\s+/g, ' '),
}))()`

/** removeAll deletes everything the run created; a 404 means it was already gone. */
async function removeAll() {
  const codes = []
  for (const [resource, path] of [
    ['pages', 'status-pages'],
    ['groups', 'monitor-groups'],
    ['templates', 'monitor-templates'],
    ['monitors', 'monitors'],
  ]) {
    for (const id of created[resource]) {
      const response = await fetch(`${url}/api/${path}/${id}`, { method: 'DELETE' }).catch(() => ({ status: 0 }))
      codes.push(response.status)
    }
  }
  return codes
}

const chrome = findChrome()
const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })

/** clickHeader clicks the sort button of a column (the index is a literal). */
const clickHeader = (index) =>
  page.evaluate(`(() => {
    const button = [...document.querySelectorAll('table thead th button')][${index}]
    if (!button) return false
    button.click()
    return true
  })()`)

/** setFilter types into the table filter, found by the placeholder the view sets. */
const setFilter = (placeholder, value) =>
  page.evaluate(`(() => {
    const input = [...document.querySelectorAll('input')].find(
      (field) => field.placeholder === ${JSON.stringify(placeholder)},
    )
    if (!input) return false
    input.value = ${JSON.stringify(value)}
    input.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  })()`)

const readTable = () => page.evaluate(SNAPSHOT)
const namesOf = (table) => table.rows.map((row) => row.name)
/** storedSort reads the remembered sort of one table out of localStorage. */
const storedSort = (key) => page.evaluate(`localStorage.getItem(${JSON.stringify(key)})`)

/**
 * checkTable drives one table: the filter and its counter, the default column,
 * both directions of a second column, the persistence and the empty state.
 */
async function checkTable(table, path, options) {
  console.log(`> ${table}`)
  await page.goto(`${url}${path}`, { settle: 1800 })

  // The filter narrows the list to the three fixtures, so the expected order is
  // always computed from them and never from whatever else is in the database.
  check(`${table}: the filter input is there`, await setFilter(options.placeholder, term))
  await sleep(400)
  const filtered = await readTable()
  const shown = filtered.rows.length
  check(`${table}: the filter keeps the three fixtures`, shown === 3, namesOf(filtered).join(', '))
  check(
    `${table}: the filter reports the shown count`,
    LOCALES.some((messages) =>
      filtered.body.includes(
        messages.common.shownOfTotal.replace('{shown}', String(shown)).replace('{total}', String(options.total)),
      ),
    ),
    `${shown} of ${options.total}`,
  )

  check(
    `${table}: the default column is ${options.defaultLabel}`,
    filtered.headers[options.defaultIndex]?.sort === 'ascending' &&
      filtered.headers[options.defaultIndex]?.label === options.defaultLabel,
    `${filtered.headers[options.defaultIndex]?.label} ${filtered.headers[options.defaultIndex]?.sort}`,
  )
  check(
    `${table}: the target column is ${options.targetLabel}`,
    filtered.headers[options.targetIndex]?.label === options.targetLabel,
    filtered.headers[options.targetIndex]?.label,
  )

  check(`${table}: clicking the header works`, await clickHeader(options.targetIndex))
  await sleep(400)
  const ascending = await readTable()
  check(
    `${table}: the column announces ascending`,
    ascending.headers[options.targetIndex]?.sort === 'ascending',
    String(ascending.headers[options.targetIndex]?.sort),
  )
  check(
    `${table}: the rows follow ascending`,
    namesOf(ascending).join('|') === options.asc.join('|'),
    namesOf(ascending).join(', '),
  )

  check(`${table}: clicking it again works`, await clickHeader(options.targetIndex))
  await sleep(400)
  const descending = await readTable()
  check(
    `${table}: the column announces descending`,
    descending.headers[options.targetIndex]?.sort === 'descending',
    String(descending.headers[options.targetIndex]?.sort),
  )
  check(
    `${table}: the rows follow descending`,
    namesOf(descending).join('|') === options.desc.join('|'),
    namesOf(descending).join(', '),
  )
  check(
    `${table}: only the active column is announced`,
    // The last header holds the row actions: it is not a sort button, so it
    // carries no aria-sort at all instead of "none".
    descending.headers.every(
      (header, index) => index === options.targetIndex || header.sort === 'none' || header.sort === null,
    ),
    descending.headers.map((header) => header.sort).join(', '),
  )

  const stored = JSON.parse((await storedSort(options.storageKey)) ?? 'null')
  check(
    `${table}: the choice is remembered under ${options.storageKey}`,
    stored?.key === options.targetKey && stored?.direction === 'desc',
    JSON.stringify(stored),
  )

  await page.goto(`${url}${path}`, { settle: 1800 })
  await setFilter(options.placeholder, term)
  await sleep(400)
  const reloaded = await readTable()
  check(
    `${table}: the choice survives a reload`,
    reloaded.headers[options.targetIndex]?.sort === 'descending' &&
      namesOf(reloaded).join('|') === options.desc.join('|'),
    namesOf(reloaded).join(', '),
  )

  await setFilter(options.placeholder, `no such ${stamp}`)
  await sleep(400)
  const empty = await readTable()
  check(`${table}: the filter hides every row`, empty.rows.length === 0, String(empty.rows.length))
  check(`${table}: the empty filter says why`, LOCALES.some((messages) => empty.body.includes(messages.common.noMatch)))

  await setFilter(options.placeholder, '')
  await sleep(300)
}

try {
  console.log(`> ${url} with ${chrome}`)

  // The profile is fresh (browser.mjs creates one per run), but clearing it is
  // cheap insurance: a warm one would carry the locale and the sort of an earlier
  // run and turn the "default column" checks red.
  await page.goto(url, { settle: 800 })
  await page.evaluate('localStorage.clear()')

  // --- fixtures through the API ---------------------------------------------
  for (const key of ['m1', 'm2']) {
    const monitor = await apiCall('/api/monitors', 'POST', {
      name: `${term} ${key}`,
      type: 'http',
      active: true,
      interval_seconds: 3600,
      timeout_seconds: 10,
      config: { url: `http://127.0.0.1:8099/${key}`, method: 'GET' },
    })
    created.monitors.push(monitor.id)
  }
  const [m1, m2] = created.monitors

  // Templates: three different intervals (the column the check sorts by).
  for (const [key, interval] of [
    ['a', 300],
    ['b', 60],
    ['c', 180],
  ]) {
    const template = await apiCall('/api/monitor-templates', 'POST', {
      name: `${term} ${key}`,
      type: 'http',
      config: { url: `http://127.0.0.1:8099/${key}`, method: 'GET' },
      defaults: { interval_seconds: interval, timeout_seconds: 10, run_on: 'all' },
    })
    created.templates.push(template.id)
  }

  // Groups: an explicit sort_order (0, 1, 2) and 2/1/0 monitors.
  for (const [key, order, members] of [
    ['a', 0, [m1, m2]],
    ['b', 1, [m1]],
    ['c', 2, []],
  ]) {
    const group = await apiCall('/api/monitor-groups', 'POST', {
      name: `${term} ${key}`,
      sort_order: order,
      monitor_ids: members,
    })
    created.groups.push(group.id)
  }

  // Status pages: 2/1/0 monitors, so the Monitors column orders c, b, a.
  for (const [key, members] of [
    ['a', [m1, m2]],
    ['b', [m1]],
    ['c', []],
  ]) {
    const statusPage = await apiCall('/api/status-pages', 'POST', {
      slug: `zz-sort-${key}-${stamp}`,
      title: `${term} ${key}`,
      theme: 'system',
      is_public: true,
      monitor_ids: members,
    })
    created.pages.push(statusPage.id)
  }

  // The counter is compared against the totals the API reports, so the check
  // holds whatever else the database already contains.
  const total = {
    templates: (await apiCall('/api/monitor-templates')).length,
    groups: (await apiCall('/api/monitor-groups')).length,
    pages: (await apiCall('/api/status-pages')).length,
  }

  const [a, b, c] = fixtureNames

  await checkTable('templates', '/admin/monitor-templates', {
    placeholder: locale.templates.filterPlaceholder,
    defaultIndex: 0,
    defaultLabel: locale.common.name,
    targetIndex: 3,
    targetLabel: locale.common.interval,
    targetKey: 'interval',
    asc: [b, c, a],
    desc: [a, c, b],
    storageKey: 'up.admin.monitor-templates.sort',
    total: total.templates,
  })
  await checkTable('groups', '/admin/monitor-groups', {
    placeholder: locale.groups.filterPlaceholder,
    defaultIndex: 2,
    defaultLabel: locale.groups.sortOrder,
    targetIndex: 1,
    targetLabel: locale.groups.monitors,
    targetKey: 'monitors',
    asc: [c, b, a],
    desc: [a, b, c],
    storageKey: 'up.admin.monitor-groups.sort',
    total: total.groups,
  })
  await checkTable('status pages', '/admin/status-pages', {
    placeholder: locale.statusPages.filterPlaceholder,
    defaultIndex: 0,
    defaultLabel: locale.common.name,
    targetIndex: 3,
    targetLabel: locale.statusPages.monitors,
    targetKey: 'monitors',
    asc: [c, b, a],
    desc: [a, b, c],
    storageKey: 'up.admin.status-pages.sort',
    total: total.pages,
  })

  check('no console errors', page.consoleMessages.length === 0, page.consoleMessages.join(' | '))
  check('no page exceptions', page.exceptions.length === 0, page.exceptions.join(' | '))
} finally {
  // The removal lives here, so a check that throws (not just fails) still leaves
  // no fixture behind: 404 answers from an earlier attempt are ignored.
  const codes = await removeAll()
  check('cleanup removed every fixture', codes.every((code) => code === 204 || code === 404), JSON.stringify(codes))
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x table sort check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok the three admin tables sort, remember the choice per table and filter')
