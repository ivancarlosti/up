#!/usr/bin/env node
/**
 * The screenshots of the README, taken from a seeded instance.
 *
 * `shots.mjs` is the layout check: it walks every page at three profiles and
 * fails when one of them overflows horizontally. This script is the publishing
 * half. It signs in with the account of `seed-demo.mjs`, walks the screens in
 * the order the README shows them, writes `docs/screenshots/N-name.png` at a
 * single profile (1600x1000, light) and prints the overflow it measured on each
 * page, so a shot of a broken layout cannot be published by accident.
 *
 * The fixture matters: on an empty instance the dashboard, the expiry page and
 * the security page all render "nothing here yet", which documents nothing.
 * `npm run seed:demo` builds the dataset these images are taken from.
 *
 * Usage:
 *   npm run shots:readme                                  # http://localhost:3000 -> docs/screenshots
 *   npm run shots:readme -- --url http://127.0.0.1:3010 --only security
 *   npm run shots:readme -- --out /tmp/up-readme-shots    # keep the tree clean
 *
 * The default address is the `APP_URL` of the local `.env`, and it matters for
 * two reasons: the real time hub accepts the upgrade only when the Origin equals
 * the configured APP_URL (cmd/server/app.go hands `cfg.AppURL` to
 * ws.NewHub), and the IP rules of the fixture have to allow the address the
 * browser connects from. `localhost` resolves to ::1 on a dual stack machine,
 * which is why the fixture allows both loopback families (127.0.0.1/32 and
 * ::1/128) - a list with only one of them makes the browser render its empty
 * state while curl over the other family still answers. The preflight below
 * stops the run in that case instead of publishing screenshots of an empty
 * instance, and the badge assertion catches the websocket half.
 *
 * Requirements: a running instance (built bundle), the credentials of the
 * account that instance authenticates with (`SHOTS_EMAIL` / `SHOTS_PASSWORD`,
 * admin@example.com / admin123 by default) and a Chrome/Chromium binary.
 */
import { existsSync, mkdirSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** Where the table of the README expects the images. */
const DEFAULT_OUT = resolve(dirname(fileURLToPath(import.meta.url)), '../../docs/screenshots')

/**
 * The profile of every shot. One width for the whole set keeps the table of the
 * README readable: mixing 1440 and 1600 wide images makes the captions jump.
 */
const PROFILE = { width: 1600, height: 1000, dark: false }

/**
 * The real time badge of the header, read the same way `smoke.mjs` does. Its
 * label is the one of the active locale, so the assertion below only holds for
 * the en-US instance the fixture is built on.
 */
const REALTIME_BADGE = `(() => {
  const el = document.querySelector('header span[title]')
  return el ? el.textContent.trim() : ''
})()`

/**
 * The static screens, in README order. The number in the file name is that
 * order, so the names stay stable when a screen is added and the README table
 * is rebuilt from this list.
 */
const SHOTS = [
  { file: '1-dashboard.png', path: '/', caption: 'Dashboard' },
  { file: '2-monitorgroups.png', path: '/admin/monitor-groups', caption: 'Monitor groups' },
  { file: '3-monitortemplates.png', path: '/admin/monitor-templates', caption: 'Monitor templates' },
  { file: '4-notifications.png', path: '/admin/notifications', caption: 'Notification channels' },
  { file: '5-tldsslexpiration.png', path: '/admin/expiry', caption: 'TLD and SSL expiration' },
  { file: '6-statuspages.png', path: '/admin/status-pages', caption: 'Status pages' },
  { file: '7-security.png', path: '/admin/security', caption: 'Security (IP rules, tokens)' },
  { file: '8-cluster.png', path: '/admin/cluster', caption: 'Cluster' },
  { file: '9-settings.png', path: '/admin/settings', caption: 'Settings' },
]

/** The monitor the detail shot follows: the flagship probe of the fixture. */
const DETAIL_MONITOR = 'Website'

/** The status page of the public shot. */
const PUBLIC_STATUS_PAGE = 'status'

function parseArgs(argv) {
  const options = {
    url: process.env.SHOTS_BASE_URL ?? 'http://localhost:3000',
    out: DEFAULT_OUT,
    only: '',
    email: process.env.SHOTS_EMAIL ?? 'admin@example.com',
    password: process.env.SHOTS_PASSWORD ?? 'admin123',
  }
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--url') options.url = argv[++i]
    else if (argv[i] === '--out') options.out = argv[++i]
    else if (argv[i] === '--only') options.only = argv[++i]
    else if (argv[i] === '--email') options.email = argv[++i]
    else if (argv[i] === '--password') options.password = argv[++i]
    else {
      console.error(`unknown option: ${argv[i]}`)
      process.exit(2)
    }
  }
  return options
}

function findChromeBinary() {
  try {
    return findChrome()
  } catch (error) {
    console.error(error.message)
    process.exit(2)
  }
}

/**
 * signIn logs in through the API and returns the session cookie pair, or an
 * empty string when the instance runs without authentication (AUTH_METHOD=none),
 * which is what the throwaway instance of docs/development.md does.
 */
async function signIn(base, email, password) {
  const response = await fetch(new URL('/api/auth/login', base), {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ email, password }),
  })
  if (response.ok) {
    const cookies = typeof response.headers.getSetCookie === 'function' ? response.headers.getSetCookie() : []
    const session = cookies.map((value) => value.split(';')[0]).find((pair) => pair.startsWith('up_session='))
    if (!session) throw new Error('the login response carried no up_session cookie')
    return session
  }

  const session = await fetch(new URL('/api/auth/session', base))
  const body = session.ok ? await session.json() : null
  if (body?.authenticated && body?.identity?.method === 'none') return ''
  throw new Error(`cannot sign in as ${email}: HTTP ${response.status}`)
}

/**
 * refusalHint turns the answer of a refused call into the sentence that fixes
 * it. The scopes of the security page are allow lists as soon as they hold an
 * allow rule (services/iprule_decision.go), so a browser that reaches the
 * instance over an address the rules do not hold gets a 403 on every scoped call
 * and renders an empty page.
 */
async function refusalHint(response) {
  const body = await response.json().catch(() => null)
  if (body?.code !== 'ERR_IP_BLOCKED') return ''
  return (
    ` (${body.message}) - a non-empty allow list turns the IP rules of a scope into an allow-list: ` +
    'allow the address the browser connects from (the fixture holds both loopback families) ' +
    'or point --url at one that is already allowed'
  )
}

/**
 * preflight proves that the API answers the operator before the shutter opens.
 * A session cookie alone is not enough: when the browser is refused by the IP
 * rules the pages render their empty state, and the run would publish
 * screenshots of a dataset that does exist.
 */
async function preflight(base, cookie) {
  const response = await fetch(new URL('/api/monitors', base), cookie === '' ? {} : { headers: { cookie } })
  if (!response.ok) {
    throw new Error(`GET /api/monitors -> HTTP ${response.status}${await refusalHint(response)}`)
  }
  const monitors = await response.json()
  if (monitors.length === 0) throw new Error('the instance has no monitor: run `npm run seed:demo` first')
  return monitors.length
}

/** dynamicPaths resolves the two screens whose route carries an id or a slug. */
async function dynamicPaths(base, cookie) {
  const headers = cookie === '' ? {} : { cookie }
  const get = async (path) => {
    const response = await fetch(new URL(path, base), { headers })
    if (!response.ok) throw new Error(`GET ${path} -> HTTP ${response.status}${await refusalHint(response)}`)
    return response.json()
  }

  const monitors = await get('/api/monitors')
  const monitor = monitors.find((row) => row.name === DETAIL_MONITOR) ?? monitors[0]
  if (!monitor) throw new Error('the instance has no monitor to show: run `npm run seed:demo`')

  const pages = await get('/api/status-pages')
  const list = Array.isArray(pages) ? pages : (pages?.items ?? [])
  const page = list.find((row) => row.slug === PUBLIC_STATUS_PAGE) ?? list.find((row) => row.is_public) ?? list[0]
  if (!page) throw new Error('the instance has no status page to show: run `npm run seed:demo`')

  return [
    { file: '10-monitordetail.png', path: `/monitors/${monitor.id}`, caption: `Monitor: ${monitor.name}` },
    { file: '11-statuspage.png', path: `/status/${page.slug}`, caption: 'Public status page' },
  ]
}

/** measure reports what the page currently holds (and how wide it is). */
async function measure(page) {
  return page.evaluate(`(() => {
    const root = document.documentElement
    const text = (document.querySelector('main') ?? document.body).innerText
    return {
      overflow: root.scrollWidth - window.innerWidth,
      rows: document.querySelectorAll('table tbody tr').length,
      cards: document.querySelectorAll('section.rounded-xl').length,
      characters: text.trim().length,
      busy: document.querySelectorAll('.animate-spin').length,
    }
  })()`)
}

/**
 * waitForRender polls until the view has text and no button is still spinning.
 * A fixed delay alone publishes half rendered tables: the API answers before the
 * chart of the monitor detail has its series.
 */
async function waitForRender(page, { timeout = 20000, minimum = 300 } = {}) {
  const deadline = Date.now() + timeout
  let last = null
  while (Date.now() < deadline) {
    last = await measure(page)
    if (last.busy === 0 && last.characters >= minimum) return last
    await sleep(250)
  }
  throw new Error(`the page never finished rendering (last measure: ${JSON.stringify(last)})`)
}

async function main() {
  const options = parseArgs(process.argv.slice(2))
  const base = options.url.replace(/\/$/, '')
  const chrome = findChromeBinary()
  if (!existsSync(options.out)) mkdirSync(options.out, { recursive: true })

  const cookie = await signIn(base, options.email, options.password)
  console.log(`> ${base}${cookie === '' ? ' (no authentication)' : ` as ${options.email}`}`)

  // The shutter only opens when the API answers the operator: an instance that
  // refuses the browser (see refusalHint) would otherwise be published as a set
  // of empty pages.
  console.log(`> preflight: ${await preflight(base, cookie)} monitor(s) visible to the API`)

  const shots = [...SHOTS, ...(await dynamicPaths(base, cookie))].filter(
    (shot) => !options.only || shot.file.includes(options.only) || shot.caption.toLowerCase().includes(options.only),
  )
  if (shots.length === 0) {
    console.error(`no screen matches --only ${options.only}`)
    process.exit(2)
  }

  console.log(`> ${shots.length} screen(s) at ${PROFILE.width}x${PROFILE.height} -> ${options.out}`)
  const browser = await launch({ chrome, width: PROFILE.width, height: PROFILE.height })
  const page = await newPage(browser, { ...PROFILE })

  const failures = []
  const table = []
  try {
    // The cookie is installed through the DevTools protocol instead of driving
    // the login form: the capture then does not depend on the labels of a form
    // that the active locale translates.
    await page.goto(`${base}/login`, { settle: 200 })
    if (cookie !== '') {
      const separator = cookie.indexOf('=')
      await page.send('Network.setCookie', {
        url: base,
        name: cookie.slice(0, separator),
        value: cookie.slice(separator + 1),
      })
      const status = await page.evaluate("fetch('/api/auth/session', { credentials: 'same-origin' }).then((r) => r.status)")
      if (status !== 200) throw new Error(`the browser session was rejected: /api/auth/session -> HTTP ${status}`)
    }

    for (const shot of shots) {
      await page.goto(`${base}${shot.path}`, { settle: 800 })
      await waitForRender(page)
      // One more beat for the charts to finish their transition and for the
      // first websocket update to land before the shutter.
      await sleep(900)
      const metrics = await measure(page)
      const badge = await page.evaluate(REALTIME_BADGE)
      await page.screenshot(join(options.out, shot.file))
      if (metrics.overflow > 1) failures.push(`${shot.file} overflows by ${metrics.overflow}px`)
      // The dashboard is the one screen where the header badge is the first
      // thing a reader looks at, and a badge that reads anything but Live means
      // the real time channel of this browser never connected.
      if (shot.path === '/' && badge !== 'Live') {
        failures.push(
          `${shot.file} shows the real time badge "${badge || '(hidden)'}" instead of Live: the ` +
            `websocket upgrade to ${base}/api/ws was refused (the hub only accepts the Origin of APP_URL)`,
        )
      }
      table.push({ ...shot, overflow: metrics.overflow, rows: metrics.rows, cards: metrics.cards, badge })
    }
  } finally {
    await page.close()
    browser.close()
  }

  for (const row of table) {
    const flag = row.overflow > 1 ? 'OVERFLOW' : 'ok'
    console.log(
      `  ${row.file.padEnd(24)} ${flag.padEnd(9)} overflow=${String(row.overflow).padEnd(5)} rows=${String(row.rows).padEnd(4)} cards=${String(row.cards).padEnd(4)} badge=${row.badge || '(hidden)'}`,
    )
  }

  if (failures.length > 0) {
    console.error(`x ${failures.length} layout problem(s):`)
    failures.forEach((failure) => console.error(`   - ${failure}`))
    process.exit(1)
  }
  console.log(`ok ${table.length} screenshot(s) written to ${options.out}`)
}

main().catch((error) => {
  console.error(error.message)
  process.exit(1)
})
