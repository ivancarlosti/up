#!/usr/bin/env node
/**
 * Shot of the "Page order" panel of the status page dialog.
 *
 * The panel is the only place the manual order of a page can be inspected, and it
 * is a dialog: no route of the application renders it, so the README screenshot of
 * `npm run shots` cannot reach it either. This script opens the page editor of a
 * given status page, scrolls the panel into view and captures it in the light and
 * the dark theme — plus the "preview" state, which is what shows an operator what
 * "alphabetical" or "by type" does.
 *
 * It needs a running instance that already has a status page with a linked group
 * (see /tmp/seed-order.sh in the commit message, or `npm run seed:demo`), open to
 * the local administrator: the script does not log in.
 *
 * Usage:
 *   npm run build
 *   node scripts/shot-status-page-order.mjs --url http://localhost:3010 --slug service-status
 */
import { mkdirSync } from 'node:fs'
import { join } from 'node:path'

import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

function parseArgs(argv) {
  const options = { url: 'http://localhost:3000', slug: 'service-status', out: '/tmp/up-shots' }
  for (let i = 0; i < argv.length; i++) {
    if (argv[i] === '--url') options.url = argv[++i] ?? options.url
    else if (argv[i] === '--slug') options.slug = argv[++i] ?? options.slug
    else if (argv[i] === '--out') options.out = argv[++i] ?? options.out
    else {
      console.error(`unknown option: ${argv[i]}`)
      process.exit(2)
    }
  }
  return options
}

/** applyTheme stores the preference the app reads on boot and repaints the document. */
async function applyTheme(page, mode) {
  await page.evaluate(`(() => {
    localStorage.setItem('up.theme', ${JSON.stringify(mode)})
    document.documentElement.classList.toggle('dark', ${JSON.stringify(mode === 'dark')})
    document.documentElement.dataset.theme = ${JSON.stringify(mode)}
  })()`)
  await sleep(300)
}

/** openPageEditor clicks the edit button of the row of the page and waits for the dialog. */
async function openPageEditor(page, slug) {
  const result = await page.evaluate(`(() => {
    const rows = [...document.querySelectorAll('tbody tr')]
    const row = rows.find((current) => current.textContent.includes('/status/' + ${JSON.stringify(slug)}))
    if (!row) return 'row not found'
    const button = row.querySelector('button[aria-label="Edit"]') || row.querySelectorAll('button')[4]
    if (!button) return 'edit button not found'
    button.click()
    return 'ok'
  })()`)
  if (result !== 'ok') throw new Error(`could not open the editor: ${result}`)
  await sleep(1500)
}

/** focusPanel scrolls the page order panel into the middle of the viewport. */
async function focusPanel(page) {
  await page.evaluate(`(() => {
    const panel = document.querySelector('#page-order')
    if (!panel) return 'panel not found'
    panel.scrollIntoView({ block: 'center' })
    return 'ok'
  })()`)
  await sleep(500)
}

const options = parseArgs(process.argv.slice(2))
const base = options.url.replace(/\/$/, '')
mkdirSync(options.out, { recursive: true })

const chrome = findChrome()
const browser = await launch({ chrome, width: 1280, height: 1000 })
const page = await newPage(browser, { width: 1280, height: 1000 })

try {
  console.log(`> ${base} (${chrome})`)

  await page.goto(`${base}/admin/status-pages`, { settle: 1500 })
  await applyTheme(page, 'light')
  await page.goto(`${base}/admin/status-pages`, { settle: 1500 })
  await openPageEditor(page, options.slug)
  await focusPanel(page)

  const panel = await page.evaluate(`(() => {
    const section = document.querySelector('#page-order')
    if (!section) return null
    const mode = document.querySelector('#page-monitor-order')
    return {
      rows: section.querySelectorAll('[draggable]').length,
      blocks: section.querySelectorAll('div.grid.gap-1').length,
      mode: mode ? mode.value : '',
    }
  })()`)
  if (!panel) throw new Error('the page order panel is not in the dialog')
  console.log(`  panel blocks=${panel.blocks} rows=${panel.rows} mode=${panel.mode}`)
  if (panel.rows === 0) throw new Error('the panel renders no row: seed a page with monitors and a group')

  const light = join(options.out, 'status-page-order-light.png')
  await page.screenshot(light)
  console.log(`  ${light}`)

  // The preview of the effective order: it is the only way to see what the mode of
  // the page does without saving it, so it gets its own shot.
  await page.evaluate(`(() => {
    const preview = [...document.querySelectorAll('#page-order button[role="switch"]')][0]
    if (preview) preview.click()
  })()`)
  await sleep(600)
  await focusPanel(page)
  const preview = join(options.out, 'status-page-order-preview.png')
  await page.screenshot(preview)
  console.log(`  ${preview}`)

  await page.evaluate(`(() => {
    const close = [...document.querySelectorAll('button[aria-label="Close"], button[aria-label="Cancel"]')][0]
    if (close) close.click()
  })()`)
  await sleep(400)

  // The dark theme of the same panel, the way the README shows every other view.
  await applyTheme(page, 'dark')
  await openPageEditor(page, options.slug)
  await focusPanel(page)
  const dark = join(options.out, 'status-page-order-dark.png')
  await page.screenshot(dark)
  console.log(`  ${dark}`)

  if (page.exceptions.length > 0) {
    console.error(`x ${page.exceptions.length} uncaught exception(s):`)
    page.exceptions.forEach((exception) => console.error(`   - ${exception}`))
    process.exitCode = 1
  } else {
    console.log('ok the panel rendered without an exception')
  }
} finally {
  await page.close()
  browser.close()
}
