/**
 * shots.mjs - visual and layout check of every screen.
 *
 * Captures the dashboard, the monitor detail and the admin pages at three widths
 * (desktop, laptop, phone) in light and dark, and reports two things that are easy
 * to miss by eye: any horizontal overflow (a grid child without min-w-0, a table
 * that is too wide) and the text of the real time badge.
 *
 * Usage:
 *   npm run build && npm run shots                       # starts nothing, needs a URL
 *   npm run shots -- --url http://localhost:3000 --out /tmp/up-shots
 *   npm run shots -- --url http://localhost:3000 --only dashboard
 */
import { existsSync, mkdirSync } from 'node:fs'
import { join } from 'node:path'
import { findChrome, launch, newPage } from './browser.mjs'

const ROUTES = [
  { path: '/', name: 'dashboard' },
  { path: '/admin/monitors', name: 'admin-monitors' },
  { path: '/admin/monitor-groups', name: 'admin-monitor-groups' },
  { path: '/admin/notifications', name: 'admin-notifications' },
  { path: '/admin/status-pages', name: 'admin-status-pages' },
  { path: '/admin/security', name: 'admin-security' },
  { path: '/admin/cluster', name: 'admin-cluster' },
  { path: '/admin/settings', name: 'admin-settings' },
  { path: '/admin/about', name: 'admin-about' },
  { path: '/login', name: 'login' },
]

const PROFILES = [
  { width: 1600, height: 1000, dark: false, label: 'desktop' },
  { width: 1440, height: 900, dark: true, label: 'dark' },
  { width: 390, height: 844, dark: false, label: 'mobile' },
]

function parseArgs(argv) {
  const options = { url: 'http://localhost:3000', out: '/tmp/up-shots', only: '' }
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--url') options.url = argv[++i]
    else if (argv[i] === '--out') options.out = argv[++i]
    else if (argv[i] === '--only') options.only = argv[++i]
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

/** monitorRoutes returns the detail route of the first monitor of the instance. */
async function monitorRoutes(base) {
  try {
    const response = await fetch(new URL('/api/monitors', base))
    if (!response.ok) return []
    const monitors = await response.json()
    if (!Array.isArray(monitors) || monitors.length === 0) return []
    return [{ path: `/monitors/${monitors[0].id}`, name: 'monitor-detail' }]
  } catch {
    return []
  }
}

async function main() {
  const options = parseArgs(process.argv.slice(2))
  const base = options.url.replace(/\/$/, '')
  const chrome = findChromeBinary()
  if (!existsSync(options.out)) mkdirSync(options.out, { recursive: true })

  const routes = [...ROUTES, ...(await monitorRoutes(base))].filter(
    (route) => !options.only || route.name.includes(options.only),
  )

  console.log(`> ${routes.length} routes x ${PROFILES.length} profiles -> ${options.out}`)
  const browser = await launch({ chrome, width: PROFILES[0].width, height: PROFILES[0].height })
  const page = await newPage(browser, { width: PROFILES[0].width, height: PROFILES[0].height })

  const failures = []
  const table = []
  try {
    for (const profile of PROFILES) {
      await page.viewport(profile)
      for (const route of routes) {
        await page.goto(`${base}${route.path}`, { settle: profile.width < 600 ? 900 : 1200 })
        const metrics = await page.evaluate(`(() => {
          const root = document.documentElement
          const badge = document.querySelector('header span[title]')
          return {
            overflow: root.scrollWidth - window.innerWidth,
            badge: badge ? badge.textContent.trim() : '',
            cards: document.querySelectorAll('section.rounded-xl').length,
          }
        })()`)
        const file = join(options.out, `${route.name}-${profile.width}-${profile.dark ? 'dark' : 'light'}.png`)
        await page.screenshot(file)
        const bad = metrics.overflow > 1
        if (bad) failures.push(`${route.name} @${profile.width}px overflows by ${metrics.overflow}px`)
        table.push({
          route: route.name,
          profile: `${profile.width}x${profile.height}${profile.dark ? ' dark' : ''}`,
          overflow: metrics.overflow,
          badge: metrics.badge,
          cards: metrics.cards,
        })
      }
    }
  } finally {
    await page.close()
    browser.close()
  }

  for (const row of table) {
    const flag = row.overflow > 1 ? 'OVERFLOW' : 'ok'
    console.log(
      `  ${row.route.padEnd(22)} ${row.profile.padEnd(18)} ${flag.padEnd(9)} badge="${row.badge}" cards=${row.cards}`,
    )
  }

  if (failures.length > 0) {
    console.error(`x ${failures.length} layout problem(s):`)
    failures.forEach((failure) => console.error(`   - ${failure}`))
    process.exit(1)
  }
  console.log('ok no horizontal overflow detected')
}

main().catch((error) => {
  console.error(error)
  process.exit(1)
})
