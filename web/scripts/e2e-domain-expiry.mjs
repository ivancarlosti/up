#!/usr/bin/env node
/**
 * End to end check of the domain expiration watch (P5).
 *
 * It drives the real UI and the real admin API: creates two monitors with the
 * domain watch on and a manual date, runs the daily job, checks the badge, the
 * deduplicated target list, the per-target refresh and the WHOIS rule tester,
 * then proves the admin table can be sorted (including by expiration).
 *
 * It is hermetic on purpose: a manual date never opens a socket and the WHOIS
 * rule is tested against a PASTED response, so the suite does not depend on a
 * registry being reachable (the live RDAP/WHOIS parsing is covered by
 * internal/expiry/*_test.go with recorded fixtures).
 *
 * Usage:
 *   npm run build && npm run e2e:domain-expiry -- --url http://localhost:3000
 *
 * Needs a running instance with authentication disabled, like the other e2e
 * scripts (it lands directly on /admin/monitors).
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

function clickButton(page, pattern) {
  return page.evaluate(`(() => {
    const el = [...document.querySelectorAll('button')].find((button) => ${pattern}.test(button.textContent.trim()))
    if (!el) return false
    el.click()
    return true
  })()`)
}

function setValue(page, selector, value, event = 'input') {
  return page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)})
    if (!el) return false
    el.value = ${JSON.stringify(value)}
    el.dispatchEvent(new Event(${JSON.stringify(event)}, { bubbles: true }))
    return true
  })()`)
}

/** isoDate is the YYYY-MM-DD a native date input expects. */
function isoDate(daysFromNow) {
  return new Date(Date.now() + daysFromNow * 86400000).toISOString().slice(0, 10)
}

const url = parseArgs(process.argv.slice(2))
const chrome = findChrome()
const stamp = Date.now()
const names = [`e2e domain a ${stamp}`, `e2e domain b ${stamp}`]
const parserTLD = `e2e${stamp}`
const created = { monitors: [], parsers: [] }

/**
 * target is the URL of the monitor. It must yield a registrable domain, so the
 * suite points at the instance through its HOSTNAME: an IP normalizes to no
 * domain at all (that is a documented behaviour, not something to work around).
 */
const target = (() => {
  const parsed = new URL(url)
  const host = parsed.hostname === '127.0.0.1' ? 'localhost' : parsed.hostname
  return `${parsed.protocol}//${host}${parsed.port ? `:${parsed.port}` : ''}/api/health`
})()

const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })
const api = (expression) => page.evaluate(expression)

/** createMonitor fills the real form: target, domain switches and the date. */
async function createMonitor(name, expiresAt) {
  await page.goto(`${url}/admin/monitors`, { settle: 1500 })
  check(`the monitor dialog opens for ${name}`, await clickButton(page, /add monitor|adicionar monitor|agregar monitor/i))
  await sleep(600)
  await setValue(page, '#monitor-name', name)
  await setValue(page, '#monitor-url', target)

  const toggled = await page.evaluate(`(() => {
    const section = document.querySelector('#monitor-domain')
    if (!section) return 0
    const switches = [...section.querySelectorAll('button[role="switch"]')]
    switches[0]?.click()
    switches[1]?.click()
    return switches.length
  })()`)
  check(`${name}: the domain switches exist`, toggled === 2, String(toggled))
  await sleep(400)
  // An empty expiresAt leaves the date blank: the second monitor of the domain
  // must still end up on ONE target, and it inherits the date typed on the first
  // one (that is the "one value per registrable domain" rule).
  if (expiresAt) {
    check(`${name}: the manual date field exists`, await setValue(page, '#monitor-domain-expires', expiresAt))
  }
  await setValue(page, '#monitor-domain-warn', '30,20,19')
  await clickButton(page, /^(save|salvar|guardar)$/i)
  await sleep(1500)
}

try {
  console.log(`> ${url} with ${chrome}`)
  console.log(`  target: ${target}`)

  await createMonitor(names[0], isoDate(20))
  // The second monitor of the SAME registrable domain is created WITHOUT a date:
  // it must inherit the date of the domain, so the worklist still renders a
  // single manual row for the two of them.
  await createMonitor(names[1], '')

  const monitors = await api(`fetch('/api/monitors?decorate=false').then((r) => r.json())`)
  for (const name of names) {
    const monitor = monitors.find((item) => item.name === name)
    check(`${name}: created with the domain watches`, Boolean(monitor) && monitor.domain_watch && monitor.domain_notify)
    if (monitor) created.monitors.push(monitor.id)
  }
  const first = monitors.find((item) => item.name === names[0])
  if (!first) throw new Error('the first monitor was not created')
  check('the thresholds were stored verbatim', first.domain_warn_days === '30,20,19', first.domain_warn_days)

  // The daily job (the manual date path needs no registry lookup).
  const run = await api(`fetch('/api/admin/expiry/run', { method: 'POST' }).then((r) => r.json())`)
  check('the job ran', run.ran === true)

  const decorated = await api(`fetch('/api/monitors/' + ${first.id}).then((r) => r.json())`)
  check('the domain was captured', Boolean(decorated.domain), JSON.stringify(decorated.domain ?? {}))
  check('the manual date is the source', decorated.domain?.source === 'manual', decorated.domain?.source)
  check('the status is ok', decorated.domain?.status === 'ok', decorated.domain?.status)
  check(
    'the remaining validity is right',
    decorated.domain?.days_left === 19 || decorated.domain?.days_left === 20,
    String(decorated.domain?.days_left),
  )
  check('the registrable domain was derived', decorated.domain?.domain === 'localhost', decorated.domain?.domain)

  // The deduplicated worklist and the per-target refresh.
  const list = await api(`fetch('/api/admin/expiry/targets').then((r) => r.json())`)
  const domainTarget = (list.targets ?? []).find((target) => target.kind === 'domain' && target.key === 'localhost')
  check('the domain target is listed once', Boolean(domainTarget) && domainTarget.monitors.length === 2, JSON.stringify(list.count))
  check('the listed target carries the observation', domainTarget?.status === 'ok' && typeof domainTarget?.days_left === 'number')
  // The two monitors share the registrable domain: it is ONE target, and the
  // date typed once is inherited by the other monitor of the domain — which is
  // exactly what stopped the duplicate row in the admin list.
  check(
    'the target marks its manual source',
    (list.targets ?? []).filter((item) => item.kind === 'domain' && item.manual).length === 1 &&
      domainTarget?.manual === true &&
      domainTarget.monitors.length === 2,
    JSON.stringify(list.targets?.map((item) => [item.kind, item.manual])),
  )

  const refreshed = await api(`fetch('/api/admin/expiry/targets/refresh', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ kind: 'domain', target: 'localhost' }),
  }).then((r) => r.json())`)
  check('one target can be refreshed alone', refreshed.monitors === 2, JSON.stringify(refreshed))

  const unknown = await api(`fetch('/api/admin/expiry/targets/refresh', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ kind: 'banana', target: 'x' }),
  }).then((r) => r.status)`)
  check('an unknown kind is rejected', unknown === 400, String(unknown))

  // The WHOIS rule tester against a pasted response (no registry involved).
  const parserDraft = {
    tld: parserTLD,
    server: 'whois.example.test',
    expiry_regex: 'expires at:\\s*(.+)',
    date_layouts: '2006-01-02',
    not_found_pattern: '(?i)no match for',
    raw: 'domain: example.test\nexpires at: 2027-05-01\n',
  }
  const tested = await api(`fetch('/api/admin/expiry/whois-parsers/test', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: ${JSON.stringify(JSON.stringify(parserDraft))},
  }).then((r) => r.json())`)
  check('the rule tester parses a pasted response', tested.ok === true, String(tested.error ?? ''))
  check('the parsed date is returned', tested.expires_at === '2027-05-01T00:00:00Z', tested.expires_at)
  check('the remaining days are returned', typeof tested.days_left === 'number', String(tested.days_left))

  const createdParser = await api(`fetch('/api/admin/expiry/whois-parsers', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: ${JSON.stringify(JSON.stringify({ ...parserDraft, raw: undefined }))},
  }).then((r) => r.json())`)
  check('a rule can be stored', typeof createdParser.id === 'number', createdParser.message ?? '')
  if (typeof createdParser.id === 'number') created.parsers.push(createdParser.id)

  await page.goto(`${url}/admin/expiry`, { settle: 1500 })
  const pageText = await api(`document.body.innerText`)
  check('the admin expiry page lists the rule', pageText.includes(parserTLD))
  check('the admin expiry page lists the targets', pageText.toLowerCase().includes('localhost'))
  await page.screenshot('/tmp/up-e2e-domain-expiry.png')

  // The badge an operator looks at.
  await page.goto(`${url}/monitors/${first.id}`, { settle: 1500 })
  const badge = await api(
    `[...document.querySelectorAll('span')].map((el) => el.innerText.trim()).filter((text) => /^\\d+\\s*(days|dias|días)$/i.test(text))[0] ?? ''`,
  )
  check('the domain badge is rendered', /19|20/.test(badge), badge)

  // Sorting: the admin table sorts by any column, not only by name.
  await page.goto(`${url}/admin/monitors`, { settle: 1500 })
  const sortable = await api(`document.querySelectorAll('th[aria-sort]').length`)
  check('the table exposes sortable columns', sortable >= 7, String(sortable))

  const firstRowName = () =>
    api(`document.querySelector('tbody tr td button')?.textContent.trim() ?? ''`)
  const clickHeader = (index) =>
    api(`(() => { const th = [...document.querySelectorAll('th[aria-sort]')][${index}]; if (!th) return false; th.querySelector('button').click(); return true })()`)
  const activeDirection = () =>
    api(`document.querySelector('th[aria-sort]:not([aria-sort="none"])')?.getAttribute('aria-sort') ?? ''`)

  const initialDirection = await activeDirection()
  await clickHeader(0)
  await sleep(400)
  const afterFirstClick = await firstRowName()
  const firstDirection = await activeDirection()
  check('a click reverses the announced direction', firstDirection !== initialDirection, `${initialDirection} -> ${firstDirection}`)

  await clickHeader(0)
  await sleep(400)
  const afterSecondClick = await firstRowName()
  check('a second click restores the direction', (await activeDirection()) === initialDirection, await activeDirection())
  check('the rows follow the direction', afterFirstClick !== afterSecondClick, `${afterFirstClick} vs ${afterSecondClick}`)

  // The last sortable column is a value column (uptime): clicking it must move
  // the sort state to that header.
  await clickHeader(sortable - 1)
  await sleep(400)
  const moved = await api(`(() => {
    const list = [...document.querySelectorAll('th[aria-sort]')]
    const index = list.findIndex((th) => th.getAttribute('aria-sort') !== 'none')
    return index === list.length - 1 ? 'last' : String(index)
  })()`)
  check('any column can be sorted, not only the name', moved === 'last', moved)

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))
} finally {
  const cleanup = await api(`(async () => {
    const status = []
    for (const id of ${JSON.stringify(created.monitors)}) {
      status.push(await fetch('/api/monitors/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.parsers)}) {
      status.push(await fetch('/api/admin/expiry/whois-parsers/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return status
  })()`).catch(() => [])
  check('cleanup removed what the suite created', cleanup.every((code) => code === 204 || code === 404), JSON.stringify(cleanup))
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x domain expiration check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok the domain expiration watch (badges, targets, sorting) works end to end')
