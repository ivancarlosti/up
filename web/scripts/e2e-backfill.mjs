#!/usr/bin/env node
/**
 * End to end check of the synchronisation identity (phase 0 of
 * docs/clustering-modes.md).
 *
 * A row is addressed across databases by its `uuid`, and the last-writer-wins
 * merge is ordered by `(revision, updated_at, origin_node_id)`. Phase 0 only
 * introduces those columns, the backfill and the CLUSTER_MODE skeleton, so the
 * invariants to verify are:
 *
 *   1. CLUSTER_MODE defaults to "shared" and is reported by /api/cluster/status;
 *   2. every synchronised row (monitors, status pages, groups, templates) has a
 *      non-empty, well formed and DISTINCT uuid (an empty or duplicated uuid is
 *      what can resurrect a deleted row or drop a monitor from the sync);
 *   3. every row has revision >= 1;
 *   4. a row created after this release gets its identity from the BeforeCreate
 *      hook, without any extra step.
 *
 * `origin_node_id` is deliberately NOT asserted on a freshly created row: the
 * column is owned by database.Backfill (which stamps anything still empty on
 * every boot) and by the peer synchronisation, not by the create path. See the
 * "Data model" section of the design document.
 *
 * Idempotency of the backfill itself cannot be proven from the API: it is a
 * property of the WHERE clauses (they select only the rows still missing a
 * value) and is demonstrated by the two-boot recipe in docs/development.md.
 *
 * Usage:
 *   npm run build && npm run e2e:backfill -- --url http://localhost:3000
 */
import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

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

const failures = []
function check(label, ok, extra = '') {
  console.log(`  ${ok ? 'ok' : 'x '} ${label}${extra ? ` (${extra})` : ''}`)
  if (!ok) failures.push(label)
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

// checkIdentity applies the four invariants to one collection of rows.
function checkIdentity(label, rows) {
  const list = Array.isArray(rows) ? rows : []
  const missing = list.filter((row) => !row.uuid || !UUID_RE.test(row.uuid))
  check(`${label}: every row has a well formed uuid`, missing.length === 0,
    missing.length ? `${missing.length}/${list.length} bad` : `${list.length} rows`)

  const uuids = list.map((row) => row.uuid)
  check(`${label}: the uuids are distinct`, new Set(uuids).size === uuids.length,
    `${new Set(uuids).size}/${uuids.length} unique`)

  const badRevision = list.filter((row) => !Number.isInteger(row.revision) || row.revision < 1)
  check(`${label}: every row has revision >= 1`, badRevision.length === 0,
    badRevision.length ? `${badRevision.length} bad` : `${list.length} rows`)
}

const url = parseArgs(process.argv.slice(2))
const chrome = findChrome()
const stamp = Date.now()
const monitorNames = [`e2e backfill ${stamp} a`, `e2e backfill ${stamp} b`]
const created = { monitors: [] }

const browser = await launch({ chrome, width: 1280, height: 900 })
const page = await newPage(browser, { width: 1280, height: 900 })
const api = (expression) => page.evaluate(expression)

try {
  console.log(`> ${url} with ${chrome}`)

  // The page context carries the session cookie, exactly like the other e2e
  // scripts: navigating first is what authenticates the fetch calls below.
  await page.goto(`${url}/`, { settle: 1500 })

  // --- the cluster mode is reported ----------------------------------------
  const status = await api(`fetch('/api/cluster/status').then((r) => r.json())`)
  check('cluster status reports a mode', typeof status.mode === 'string', String(status.mode))
  check('CLUSTER_MODE defaults to shared', status.mode === 'shared', String(status.mode))

  // --- a new row gets its identity from BeforeCreate ------------------------
  for (const name of monitorNames) {
    const payload = JSON.stringify({
      name,
      type: 'http',
      active: true,
      interval_seconds: 60,
      timeout_seconds: 10,
      config: { url: 'http://127.0.0.1:8099/' },
    })
    const row = await api(`fetch('/api/monitors', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: ${JSON.stringify(payload)},
    }).then((r) => (r.ok ? r.json() : { error: r.status }))`)
    if (row && row.id) created.monitors.push(row.id)
  }
  check('two monitors created', created.monitors.length === 2, String(created.monitors.length))

  const fresh = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => m.name.startsWith('e2e backfill ${stamp}')))`)
  check('a new monitor gets a uuid without any extra step',
    fresh.length === 2 && fresh.every((m) => UUID_RE.test(m.uuid)),
    fresh.map((m) => m.uuid).join(', '))
  check('a new monitor starts at revision 1',
    fresh.every((m) => m.revision === 1), fresh.map((m) => m.revision).join(','))

  // --- the identity of everything already stored ----------------------------
  const [monitors, pages, groups, templates] = await Promise.all([
    api(`fetch('/api/monitors?decorate=false').then((r) => r.json())`),
    api(`fetch('/api/status-pages').then((r) => r.json())`),
    api(`fetch('/api/monitor-groups').then((r) => r.json())`),
    api(`fetch('/api/monitor-templates').then((r) => r.json())`),
  ])
  checkIdentity('monitors', monitors)
  checkIdentity('status pages', pages)
  checkIdentity('monitor groups', groups)
  checkIdentity('monitor templates', templates)

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))

  // --- cleanup -------------------------------------------------------------
  const cleanup = await api(`(async () => {
    const codes = []
    for (const id of ${JSON.stringify(created.monitors)}) {
      codes.push(await fetch('/api/monitors/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return codes
  })()`)
  check('cleanup removed the probes',
    cleanup.every((code) => code === 204 || code === 404), JSON.stringify(cleanup))

  await sleep(100)
} finally {
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x backfill check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok the sync identity is present, distinct and well formed on every row')
