#!/usr/bin/env node
/**
 * e2e-dashboard-load.mjs - what a dashboard load actually costs at N monitors.
 *
 * A dashboard that answers `curl` in 400 ms can still take half a minute in the
 * tab: the payload is only the first of the costs, and they get fixed in
 * different files. This script measures them apart, on a throwaway installation
 * with as many monitors as the slow one has:
 *
 *   ttfb / bytes  what the server spends and what travels (after gzip)
 *   first_row     when the first <tr> is in the DOM
 *   full_table    when the last <tr> of the list is in the DOM
 *   long_tasks    how much of the main thread the page spent in >50 ms tasks
 *   events        how many real time events arrived, and at what rate
 *
 * Every measurement runs twice on the same instance: with the live channel and
 * with it taken away before the app boots. Only the difference tells a render
 * cost of the feed apart from a render cost of the table itself.
 *
 * Usage:
 *   npm run e2e:dashboard-load -- --url http://localhost:3000 --monitors 1822
 *   npm run e2e:dashboard-load -- --monitors 1822 --reuse --label after
 *   npm run e2e:dashboard-load -- --json /tmp/before.json
 *
 * `--reuse` measures an installation that already holds the monitors (before and
 * after a change, on the same data) and `--keep` leaves the fixtures behind for
 * the next run. `--settle` (ms, default 6000) is how long each pass watches the
 * page after it loaded. The instance must run with AUTH_METHOD=none, like every
 * other e2e script, or the credentials have to come from the environment
 * (UP_E2E_EMAIL / UP_E2E_PASSWORD).
 */
import { findChrome, launch, newPage } from './browser.mjs'
import { writeFileSync } from 'node:fs'

const args = new Map()
for (let index = 2; index < process.argv.length; index += 1) {
  const arg = process.argv[index]
  if (!arg.startsWith('--')) continue
  const [key, inline] = arg.slice(2).split('=')
  const next = process.argv[index + 1]
  if (inline !== undefined) {
    args.set(key, inline)
  } else if (next && !next.startsWith('--')) {
    args.set(key, next)
    index += 1
  } else {
    args.set(key, 'true')
  }
}

const url = (args.get('url') ?? process.env.UP_URL ?? 'http://localhost:3000').replace(/\/$/, '')
const wanted = Number(args.get('monitors') ?? 1822)
const label = args.get('label') ?? 'run'
const reuse = args.has('reuse')
const keep = args.has('keep')
const settle = Number(args.get('settle') ?? 6000)
const jsonPath = args.get('json')

/**
 * PROBE is installed before the app boots, so nothing it measures is missed: a
 * MutationObserver marks the first row and the last one, and a longtask
 * PerformanceObserver sums the time the main thread was blocked.
 */
const PROBE = `(() => {
  const start = performance.now()
  const probe = { target: ${wanted}, firstRowAt: 0, fullTableAt: 0, longTasks: 0, longTaskCount: 0 }
  window.__load = probe
  try {
    new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) {
        probe.longTasks += entry.duration
        probe.longTaskCount += 1
      }
    }).observe({ entryTypes: ['longtask'] })
  } catch (error) {
    probe.longTaskError = String(error)
  }
  const rows = () => document.querySelectorAll('table tbody tr').length
  const read = () => {
    const count = rows()
    if (!probe.firstRowAt && count > 0) probe.firstRowAt = performance.now() - start
    if (!probe.fullTableAt && count >= probe.target) probe.fullTableAt = performance.now() - start
  }
  const observer = new MutationObserver(read)
  const observe = () => observer.observe(document.body, { childList: true, subtree: true })
  if (document.body) observe()
  else document.addEventListener('DOMContentLoaded', observe, { once: true })
  probe.read = () => {
    read()
    const transfer = performance
      .getEntriesByType('resource')
      .filter((entry) => entry.name.includes('/api/dashboard'))
      .reduce((total, entry) => total + (entry.transferSize || 0), 0)
    return {
      firstRowAt: Math.round(probe.firstRowAt),
      fullTableAt: Math.round(probe.fullTableAt),
      longTasks: Math.round(probe.longTasks),
      longTaskCount: probe.longTaskCount,
      rows: rows(),
      nodes: document.querySelectorAll('*').length,
      resourceKB: Math.round(transfer / 1024),
    }
  }
})()`

/**
 * NO_FEED takes the live channel away before the app boots, because
 * Network.setBlockedURLs does not stop a WebSocket upgrade: the "feed off" pass
 * would otherwise measure exactly what the "feed on" pass measured.
 *
 * It is a socket that never opens and closes once, not an object that throws:
 * the client sees a failed connection, gives up and (correctly) falls back to
 * polling, which cannot fire inside a pass. Throwing from the constructor would
 * only exercise the client's retry path, which is not what this measures.
 */
const NO_FEED = `(() => {
  class SilentWebSocket extends EventTarget {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    constructor(url) {
      super()
      this.url = String(url)
      this.readyState = 0
      this.bufferedAmount = 0
      setTimeout(() => {
        this.readyState = 3
        this.dispatchEvent(new Event('close'))
      }, 25)
    }
    send() {}
    close() {
      this.readyState = 3
    }
  }
  window.WebSocket = SilentWebSocket
})()`

/** cookie keeps the session cookie of the login pass (fetch has no jar). */
let cookie = ''

/** apiCall drives the fixtures; a 204 answers null. */
async function apiCall(path, method = 'GET', body) {
  const response = await fetch(`${url}${path}`, {
    method,
    headers: {
      ...(body ? { 'Content-Type': 'application/json' } : {}),
      ...(cookie ? { Cookie: cookie } : {}),
    },
    body: body ? JSON.stringify(body) : undefined,
  })
  const setCookie = response.headers.getSetCookie?.() ?? []
  if (setCookie.length) cookie = setCookie.map((value) => value.split(';')[0]).join('; ')
  if (!response.ok) throw new Error(`${method} ${path} -> ${response.status} ${await response.text()}`)
  return response.status === 204 ? null : response.json()
}

/** login is only needed when the instance does not run with AUTH_METHOD=none. */
async function login() {
  const email = process.env.UP_E2E_EMAIL
  const password = process.env.UP_E2E_PASSWORD
  if (!email || !password) return
  await apiCall('/api/auth/login', 'POST', { email, password })
}

const stamp = Date.now()
const prefix = `perf load ${stamp}`

/** seed creates the monitors in batches; their targets refuse instantly. */
async function seed(count) {
  const created = []
  const batch = 16
  for (let index = 0; index < count; index += batch) {
    const slice = Array.from({ length: Math.min(batch, count - index) }, (_, offset) => index + offset + 1)
    const rows = await Promise.all(
      slice.map((number) =>
        apiCall('/api/monitors', 'POST', {
          name: `${prefix} #${number}`,
          type: 'http',
          // 127.0.0.1:1 refuses instantly: the probes cost nothing, while the
          // monitor really is checked and really publishes its events, which is
          // what the live feed measurement needs.
          interval_seconds: 60,
          retries: 0,
          timeout_seconds: 5,
          resend_interval_seconds: 0,
          run_on: 'all',
          active: true,
          config: { url: 'http://127.0.0.1:1/', method: 'GET' },
        }),
      ),
    )
    created.push(...rows.map((row) => row.id))
    process.stdout.write(`\r  seeded ${created.length}/${count}`)
  }
  process.stdout.write('\n')
  return created
}

/** settleState waits until every monitor has a verdict, so the feed is real. */
async function settleState() {
  const deadline = Date.now() + 180000
  let summary = null
  while (Date.now() < deadline) {
    const dashboard = await apiCall('/api/dashboard')
    summary = dashboard.summary
    const pending = (summary.pending ?? 0) + (summary.unknown ?? 0)
    if (pending === 0) return summary
    process.stdout.write(`\r  waiting for a verdict: ${pending} pending/unknown   `)
    await new Promise((resolve) => setTimeout(resolve, 2000))
  }
  process.stdout.write('\n')
  return summary
}

/** measure runs one page load and returns everything the numbers need. */
async function measure(browser, { blockFeed }) {
  const page = await newPage(browser, { width: 1440, height: 1000 })
  await page.send('Performance.enable')
  await page.send('Page.addScriptToEvaluateOnNewDocument', { source: PROBE })
  if (blockFeed) await page.send('Page.addScriptToEvaluateOnNewDocument', { source: NO_FEED })

  const net = { frames: [], dashboard: null }
  let listening = true
  browser.onEvent((payload) => {
    if (payload.sessionId !== page.sessionId) return
    if (!listening) return
    if (payload.method === 'Network.webSocketFrameReceived') net.frames.push(Date.now())
    if (payload.method === 'Network.responseReceived' && payload.params.response.url.includes('/api/dashboard')) {
      const headers = payload.params.response.headers
      net.dashboard = {
        status: payload.params.response.status,
        ttfbMS: Math.round(payload.params.response.timing?.receiveHeadersEnd ?? -1),
        gzip: headers['Content-Encoding'] ?? headers['content-encoding'] ?? '',
      }
    }
    if (payload.method === 'Network.loadingFinished' && net.dashboard && net.dashboard.bytes === undefined) {
      net.dashboard.bytes = payload.params.encodedDataLength
    }
  })

  const started = Date.now()
  await page.goto(`${url}/`, { settle })
  const elapsed = Date.now() - started
  const probe = await page.evaluate('window.__load.read()')
  const { metrics } = await page.send('Performance.getMetrics')
  const metric = (name) => metrics.find((item) => item.name === name)?.value ?? 0
  listening = false
  await page.close()

  // The rate is counted over the whole pass, not over its last seconds: the
  // scheduler fires every monitor on its own clock, so a fixture that was seeded
  // in one batch checks in one burst per interval and a five second window can
  // legitimately land between two bursts and report nothing at all.
  const framesPerSecond = net.frames.length / Math.max(elapsed / 1000, 1)

  return {
    feed: blockFeed ? 'off' : 'on',
    status: net.dashboard?.status ?? 0,
    ttfbMS: net.dashboard?.ttfbMS ?? -1,
    gzip: net.dashboard?.gzip ?? '',
    transferKB: Math.round((net.dashboard?.bytes ?? 0) / 1024),
    firstRowMS: probe.firstRowAt,
    fullTableMS: probe.fullTableAt,
    longTasksMS: probe.longTasks,
    longTaskCount: probe.longTaskCount,
    rows: probe.rows,
    nodes: probe.nodes,
    resourceKB: probe.resourceKB,
    frames: net.frames.length,
    framesPerSecond: Number(framesPerSecond.toFixed(1)),
    layoutCount: Math.round(metric('LayoutCount')),
    recalcStyleCount: Math.round(metric('RecalcStyleCount')),
    scriptMS: Math.round(metric('ScriptDuration') * 1000),
    taskMS: Math.round(metric('TaskDuration') * 1000),
  }
}

/**
 * verifyScroll proves the whole list is still reachable. A table that mounts its
 * rows in pages must still show every monitor once the operator scrolls to the
 * bottom: this is the check that would catch a sentinel that never fires (a zero
 * sized row, a root margin that does not match the scroll container) and leave
 * the dashboard stuck at the first page.
 */
async function verifyScroll(browser, expected) {
  const page = await newPage(browser, { width: 1440, height: 1000 })
  await page.goto(`${url}/`, { settle: 2500 })
  let rows = 0
  for (let step = 0; step < 40 && rows < expected; step += 1) {
    await page.evaluate('window.scrollTo(0, document.body.scrollHeight)')
    await new Promise((resolve) => setTimeout(resolve, 150))
    rows = await page.evaluate("document.querySelectorAll('table tbody tr').length")
  }
  await page.close()
  return rows
}

async function main() {
  await login()
  const existing = (await apiCall('/api/dashboard')).summary?.total ?? 0
  let created = []
  if (reuse && existing >= wanted) {
    console.log(`reusing the ${existing} monitors already in the installation`)
  } else {
    console.log(`seeding ${wanted} monitors on ${url}`)
    created = await seed(wanted)
  }
  const summary = await settleState()
  console.log(
    `monitors=${summary?.total ?? 0} up=${summary?.up ?? 0} down=${summary?.down ?? 0} ` +
      `pending=${summary?.pending ?? 0} unknown=${summary?.unknown ?? 0}`,
  )

  const chrome = findChrome()
  const browser = await launch({ chrome, width: 1440, height: 1000 })
  const results = []
  let scrollRows = 0
  try {
    for (const blockFeed of [false, true]) {
      const result = await measure(browser, { blockFeed })
      results.push(result)
      console.log(
        `  feed=${result.feed.padEnd(3)} ttfb=${result.ttfbMS}ms bytes=${result.transferKB}kB gzip=${result.gzip || 'no'} ` +
          `first_row=${result.firstRowMS}ms full_table=${result.fullTableMS}ms ` +
          `long_tasks=${result.longTaskCount}/${result.longTasksMS}ms rows=${result.rows} nodes=${result.nodes} ` +
          `events=${result.frames} (${result.framesPerSecond}/s)`,
      )
    }
    scrollRows = await verifyScroll(browser, summary?.total ?? 0)
    console.log(`  scrolling from top to bottom reaches ${scrollRows} of ${summary?.total ?? 0} rows`)
  } finally {
    await browser.close()
    if (created.length && !keep) {
      for (let index = 0; index < created.length; index += 16) {
        await Promise.all(created.slice(index, index + 16).map((id) => apiCall(`/api/monitors/${id}`, 'DELETE')))
      }
      console.log(`removed ${created.length} fixtures`)
    }
  }

  const report = { label, url, monitors: summary?.total ?? 0, scrollRows, at: new Date().toISOString(), results }
  if (jsonPath) {
    writeFileSync(jsonPath, `${JSON.stringify(report, null, 2)}\n`)
    console.log(`wrote ${jsonPath}`)
  }
  console.log(
    `\n${label}: ${results.map((row) => `feed ${row.feed} -> first row ${row.firstRowMS} ms, table ${row.fullTableMS} ms`).join(' | ')}`,
  )
}

await main()
