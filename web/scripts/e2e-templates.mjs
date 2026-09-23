#!/usr/bin/env node
/**
 * End to end check of the monitor templates, the bulk importer and the bulk edit.
 *
 * It drives the real UI: creates a template, adds two monitors by pasting
 * "name,url" in the bulk dialog, applies the template to one of them and cleans
 * up everything it created.
 *
 * Usage:
 *   npm run build && npm run e2e:templates -- --url http://localhost:3000
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

const url = parseArgs(process.argv.slice(2))
const chrome = findChrome()
const stamp = Date.now()
const templateName = `e2e template ${stamp}`
const monitorNames = [`e2e bulk ${stamp} a`, `e2e bulk ${stamp} b`]
const created = { monitors: [], templates: [] }

const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })
const api = (expression) => page.evaluate(expression)


try {
  console.log(`> ${url} with ${chrome}`)

  // --- create a template through the UI ------------------------------------
  await page.goto(`${url}/admin/monitor-templates`, { settle: 1500 })
  check('templates page opens', (await api(`document.body.innerText`)).toLowerCase().includes('template'))
  check('new template dialog opens', await clickButton(page, /new template|novo template|nueva plantilla/i))
  await sleep(500)
  await setValue(page, '#template-name', templateName)
  await setValue(page, '#template-interval', '120')
  check('template saved', await clickButton(page, /^(save|salvar|guardar)$/i))
  await sleep(1500)
  const template = await api(`fetch('/api/monitor-templates')
    .then((r) => r.json())
    .then((list) => list.find((t) => t.name === ${JSON.stringify(templateName)}) ?? null)`)
  check('template persisted', Boolean(template))
  if (template) {
    created.templates.push(template.id)
    check('template interval saved', template.defaults.interval_seconds === 120, String(template.defaults.interval_seconds))
  }
  check('template card rendered', (await api(`document.body.innerText`)).includes(templateName))

  // --- bulk add through the UI ---------------------------------------------
  await page.goto(`${url}/admin/monitors`, { settle: 1500 })
  check('bulk dialog opens', await clickButton(page, /add in bulk|adicionar em lote|agregar en lote/i))
  await sleep(600)
  const text = `${monitorNames[0]},http://127.0.0.1:8099/${stamp}-a,,,,30\n${monitorNames[1]},http://127.0.0.1:8099/${stamp}-b,,,,30\n`
  await setValue(page, '#bulk-text', text)
  check('preview requested', await clickButton(page, /preview|pré-visualizar|vista previa/i))
  await sleep(1500)
  const preview = await api(`document.body.innerText`)
  check('preview reports two ready rows', /(Ready|Prontas|Listas): 2/.test(preview))
  check('monitors created', await clickButton(page, /^(create|criar|crear)$/i))
  await sleep(2000)
  const bulk = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => m.name.startsWith('e2e bulk ${stamp}')).map((m) => ({ id: m.id, name: m.name, interval: m.interval_seconds })))`)
  check('two monitors created', bulk.length === 2, bulk.map((m) => m.name).join(', '))
  check(
    'the row interval won over the template one',
    bulk.every((m) => m.interval === 30),
    bulk.map((m) => m.interval).join(','),
  )
  created.monitors.push(...bulk.map((m) => m.id))
  await page.screenshot('/tmp/up-e2e-bulk.png')
  // The bulk dialog stays open on purpose (it shows the per row report): close it
  // before touching the toolbar behind the modal.
  check('bulk dialog closed', await clickButton(page, /^(close|fechar|cerrar)$/i))
  await sleep(400)

  // --- apply the template to the new monitors ------------------------------
  check('apply dialog opens', await clickButton(page, /apply template|aplicar template|aplicar plantilla/i))
  await sleep(600)
  const selected = await api(`(() => {
    const prefix = ${JSON.stringify(monitorNames[0].replace(/ b$| a$/, ''))}
    const labels = [...document.querySelectorAll('[role="dialog"] label')]
      .filter((el) => el.innerText.trim().startsWith(prefix))
    let clicked = 0
    for (const label of labels) {
      label.querySelector('button[role="checkbox"], button')?.click()
      clicked++
    }
    return clicked
  })()`)
  check('the two monitors are selected', selected === 2, String(selected))
  check('preview requested', await clickButton(page, /preview|pré-visualizar|vista previa/i))
  await sleep(1500)
  const diff = await api(`document.body.innerText`)
  check('the preview shows the interval change', diff.includes('interval_seconds'), '')
  check('apply confirmed', await clickButton(page, /^(apply|aplicar)$/i))
  await sleep(2000)
  const after = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => m.name.startsWith('e2e bulk ${stamp}')).map((m) => ({ interval: m.interval_seconds, url: m.config.url })))`)
  check('the template was applied', after.every((m) => m.interval === 120), JSON.stringify(after.map((m) => m.interval)))
  check(
    'the targets survived the bulk edit',
    after.every((m) => String(m.url).startsWith('http://127.0.0.1:8099/')),
    after.map((m) => m.url).join(', '),
  )

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))

  // --- cleanup -------------------------------------------------------------
  const cleanup = await api(`(async () => {
    const status = { monitors: [], templates: [] }
    for (const id of ${JSON.stringify(created.monitors)}) {
      status.monitors.push(await fetch('/api/monitors/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.templates)}) {
      status.templates.push(await fetch('/api/monitor-templates/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return status
  })()`)
  check(
    'cleanup removed everything',
    [...cleanup.monitors, ...cleanup.templates].every((code) => code === 204 || code === 404),
    JSON.stringify(cleanup),
  )
} finally {
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x templates check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok monitor templates, the bulk importer and the bulk edit work end to end')
