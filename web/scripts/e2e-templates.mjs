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

/**
 * clickInRow clicks a button of the table row whose first cell holds the given
 * name. The action buttons of a row are icon only, so the accessible name (title
 * or aria-label) counts as text too.
 */
function clickInRow(page, name, pattern) {
  return page.evaluate(`(() => {
    const matches = (button) => {
      const text = (button.textContent ?? '').trim()
      return ${pattern}.test(text) ||
        ${pattern}.test(button.getAttribute('title') ?? '') ||
        ${pattern}.test(button.getAttribute('aria-label') ?? '')
    }
    const row = [...document.querySelectorAll('table tbody tr')].find(
      (tr) => ((tr.querySelector('td')?.innerText ?? '').split('\\n')[0] ?? '').trim() === ${JSON.stringify(name)},
    )
    const button = row && [...row.querySelectorAll('button')].find(matches)
    if (!button) return false
    button.click()
    return true
  })()`)
}

/**
 * rowCount reads the follower count cell of the template row (the third column
 * of the templates table).
 */
function rowCount(page, name) {
  return page.evaluate(`(() => {
    const row = [...document.querySelectorAll('table tbody tr')].find(
      (tr) => ((tr.querySelector('td')?.innerText ?? '').split('\\n')[0] ?? '').trim() === ${JSON.stringify(name)},
    )
    return row?.querySelectorAll('td')[2]?.innerText?.trim() ?? ''
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
  check('template row rendered', (await api(`document.body.innerText`)).includes(templateName))

  // --- edit the template through the UI ------------------------------------
  // Regression guard: the update marshals the JSON columns by hand, so a raw
  // struct used to reach the driver and every single save answered 500.
  const editedName = `${templateName} edited`
  check('edit dialog opens', await clickInRow(page, templateName, /^(edit|editar)$/i))
  await sleep(600)
  await setValue(page, '#template-name', editedName)
  await setValue(page, '#template-interval', '240')
  // The propagate switch is the last one of the template dialog (the form holds
  // one switch per capability, plus this one).
  const toggled = await api(`(() => {
    const dialog = document.querySelector('#template-interval')?.closest('[role="dialog"]')
    const switches = [...(dialog?.querySelectorAll('button[role="switch"]') ?? [])]
    const button = switches.at(-1)
    if (!button) return false
    button.click()
    return true
  })()`)
  check('the propagate switch toggles', toggled)
  check('the edit is saved', await clickButton(page, /^(save|salvar|guardar)$/i))
  await sleep(1500)
  const edited = await api(`fetch('/api/monitor-templates')
    .then((r) => r.json())
    .then((list) => list.find((t) => t.id === ${template.id}) ?? null)`)
  check('the edit persisted', edited?.name === editedName, String(edited?.name))
  check(
    'the edited defaults persisted',
    edited?.defaults?.interval_seconds === 240,
    String(edited?.defaults?.interval_seconds),
  )
  check('the propagate switch persisted', edited?.propagate === false, String(edited?.propagate))

  // --- authentication belongs to the monitor, never to the template ---------
  // The template dialog offers no auth control at all: reopen the edit dialog and
  // look for the id the monitor form uses.
  await page.goto(`${url}/admin/monitor-templates`, { settle: 1200 })
  check('the template dialog reopens', await clickInRow(page, editedName, /^(edit|editar)$/i))
  await sleep(600)
  check(
    'the template dialog hides the authentication',
    await api(`(() => {
      const dialog = document.querySelector('[role="dialog"]')
      return Boolean(dialog) && !dialog.querySelector('#monitor-auth')
    })()`),
  )
  check('the template dialog closes', await clickButton(page, /^(cancel|cancelar)$/i))
  await sleep(400)

  // A client that still sends credentials (an older UI, a script) cannot store
  // them: they are stripped on write and on read.
  const templatePut = (interval) => ({
    name: editedName,
    description: '',
    type: 'http',
    propagate: true,
    config: { method: 'POST', auth_type: 'basic', basic_user: 'operator', basic_pass: 'hunter2', bearer_token: 'token' },
    defaults: { ...(edited?.defaults ?? {}), interval_seconds: interval },
  })
  const putTemplate = (interval) => `fetch('/api/monitor-templates/${template.id}', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: ${JSON.stringify(JSON.stringify(templatePut(interval)))},
  }).then((r) => (r.ok ? r.json() : r.status))`
  const stored = await api(putTemplate(300))
  check('a template write with credentials is accepted', stored?.id === template?.id, JSON.stringify(stored))
  const storedConfig = stored?.config ?? {}
  check(
    'the stored template has no credentials',
    !storedConfig.basic_user && !storedConfig.basic_pass && !storedConfig.bearer_token && storedConfig.auth_type === 'none',
    JSON.stringify(storedConfig),
  )

  // Two monitors follow that template with different credentials: editing the
  // template propagates its defaults to both and keeps what each one holds.
  const authMonitorNames = [`e2e auth ${stamp} a`, `e2e auth ${stamp} b`]
  const authMonitors = await api(`(async () => {
    const made = []
    for (const [index, user] of [['a', 'alice'], ['b', 'bob']]) {
      const response = await fetch('/api/monitors', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name: 'e2e auth ${stamp} ' + index,
          type: 'http',
          active: false,
          interval_seconds: 60,
          timeout_seconds: 10,
          template_uuid: ${JSON.stringify(template?.uuid ?? '')},
          config: {
            url: 'https://127.0.0.1:8099/${stamp}-' + index,
            method: 'GET',
            auth_type: 'basic',
            basic_user: user,
            basic_pass: 'secret-' + user,
          },
        }),
      })
      const body = await response.json()
      made.push({ status: response.status, id: body.id, user: body.config?.basic_user, pass: body.config?.basic_pass, interval: body.interval_seconds })
    }
    return made
  })()`)
  check(
    'two monitors hold their own credentials',
    authMonitors.length === 2 &&
      authMonitors[0]?.user === 'alice' && authMonitors[0]?.pass === 'secret-alice' &&
      authMonitors[1]?.user === 'bob' && authMonitors[1]?.pass === 'secret-bob',
    JSON.stringify(authMonitors),
  )
  created.monitors.push(...authMonitors.map((monitor) => monitor.id).filter(Boolean))

  // The listing counts the followers of every template (the monitors whose
  // `template_uuid` is the template uuid) in one grouped query.
  const followerCount = await api(`fetch('/api/monitor-templates')
    .then((r) => r.json())
    .then((list) => list.find((t) => t.id === ${template.id}) ?? null)`)
  check('the template reports its followers', followerCount?.monitor_count === 2, String(followerCount?.monitor_count))

  const propagated = await api(putTemplate(600))
  check('the second template write is accepted', propagated?.id === template?.id, JSON.stringify(propagated))
  const afterPropagation = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => ${JSON.stringify(authMonitorNames)}.includes(m.name))
      .map((m) => ({ name: m.name, user: m.config.basic_user, pass: m.config.basic_pass, interval: m.interval_seconds, method: m.config.method })))`)
  check(
    'the template edit reached the linked monitors',
    afterPropagation.length === 2 && afterPropagation.every((m) => m.interval === 600),
    JSON.stringify(afterPropagation),
  )
  check(
    'the propagation kept the credentials of each monitor',
    afterPropagation.some((m) => m.user === 'alice' && m.pass === 'secret-alice') &&
      afterPropagation.some((m) => m.user === 'bob' && m.pass === 'secret-bob'),
    JSON.stringify(afterPropagation),
  )
  // Put the template back where the UI edit above left it (the bulk edit below
  // asserts on that interval).
  check('the template interval is restored', (await api(putTemplate(240)))?.defaults?.interval_seconds === 240)


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
  check('the template was applied', after.every((m) => m.interval === 240), JSON.stringify(after.map((m) => m.interval)))
  check(
    'the targets survived the bulk edit',
    after.every((m) => String(m.url).startsWith('http://127.0.0.1:8099/')),
    after.map((m) => m.url).join(', '),
  )

  // --- the ssl type and the type column of a row ---------------------------
  // The templates page used to offer only http/keyword/tcp/dns (no ssl blueprint
  // could be created) and the bulk importer knew only the http/tcp/dns target
  // shapes while the type column of a row was rejected by the template link
  // check: a bulk import could only ever create http monitors.
  const sslTemplateName = `e2e ssl template ${stamp}`
  const sslMonitorName = `e2e ssl ${stamp}`
  const overrideName = `e2e override ${stamp}`

  await page.goto(`${url}/admin/monitor-templates`, { settle: 1200 })
  // The follower count column must render exactly what the API reports (the
  // monitors linked above still follow this template).
  const expectedCount = await api(`fetch('/api/monitor-templates')
    .then((r) => r.json())
    .then((list) => list.find((t) => t.name === ${JSON.stringify(editedName)})?.monitor_count ?? -1)`)
  check('the linked template counts its followers', expectedCount >= 2, String(expectedCount))
  check(
    'the monitors column shows the follower count',
    String(expectedCount) === (await rowCount(page, editedName)),
    `${expectedCount} vs ${await rowCount(page, editedName)}`,
  )
  check('new template dialog opens for ssl', await clickButton(page, /new template|novo template|nueva plantilla/i))
  await sleep(500)
  await setValue(page, '#template-name', sslTemplateName)
  await setValue(page, '#template-type', 'ssl', 'change')
  await sleep(300)
  const sslDialog = await api(`document.querySelector('[role="dialog"]')?.innerText ?? ''`)
  check('the ssl template offers the certificate switches', /certificat|certificado/i.test(sslDialog))
  check('ssl template saved', await clickButton(page, /^(save|salvar|guardar)$/i))
  await sleep(1500)
  const sslTemplate = await api(`fetch('/api/monitor-templates')
    .then((r) => r.json())
    .then((list) => list.find((t) => t.name === ${JSON.stringify(sslTemplateName)}) ?? null)`)
  check('the ssl template persisted with its type', sslTemplate?.type === 'ssl', String(sslTemplate?.type))
  if (sslTemplate) created.templates.push(sslTemplate.id)

  // The ssl template imports host:port rows.
  await page.goto(`${url}/admin/monitors`, { settle: 1200 })
  check('bulk dialog opens for the ssl template', await clickButton(page, /add in bulk|adicionar em lote|agregar en lote/i))
  await sleep(600)
  if (sslTemplate) {
    await setValue(page, '#bulk-template', String(sslTemplate.id), 'change')
    await setValue(page, '#bulk-text', `${sslMonitorName},127.0.0.1:8443\n`)
    await sleep(500)
    check('the ssl row is ready', /(Ready|Prontas|Listas): 1/.test(await api(`document.body.innerText`)))
    check('the ssl monitor is created', await clickButton(page, /^(create|criar|crear)$/i))
    await sleep(1500)
  }

  // The type column of a row overrides the type of its template (the monitor is
  // then not linked to it: a template never describes another probe type).
  await page.goto(`${url}/admin/monitors`, { settle: 1200 })
  check('bulk dialog opens for the override', await clickButton(page, /add in bulk|adicionar em lote|agregar en lote/i))
  await sleep(600)
  await setValue(page, '#bulk-template', String(template?.id ?? ''), 'change')
  await setValue(page, '#bulk-text', `${overrideName},127.0.0.1:9000,tcp\n`)
  await sleep(500)
  check('the tcp override row is ready', /(Ready|Prontas|Listas): 1/.test(await api(`document.body.innerText`)))
  check('the override monitor is created', await clickButton(page, /^(create|criar|crear)$/i))
  await sleep(2000)
  const imported = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => [${JSON.stringify(sslMonitorName)}, ${JSON.stringify(overrideName)}].includes(m.name))
      .map((m) => ({ id: m.id, name: m.name, type: m.type, template_uuid: m.template_uuid, host: m.config.host, port: m.config.port })))`)
  const sslMonitor = imported.find((m) => m.name === sslMonitorName)
  const overrideMonitor = imported.find((m) => m.name === overrideName)
  check('the ssl monitor was imported', sslMonitor?.type === 'ssl' && sslMonitor?.port === 8443, JSON.stringify(sslMonitor))
  check('the ssl monitor follows its template', Boolean(sslMonitor?.template_uuid), String(sslMonitor?.template_uuid))
  check('the overridden monitor was imported', overrideMonitor?.type === 'tcp' && overrideMonitor?.port === 9000, JSON.stringify(overrideMonitor))
  check('an overridden row is not linked to the template', !overrideMonitor?.template_uuid, String(overrideMonitor?.template_uuid))
  created.monitors.push(...imported.map((m) => m.id))
  check('bulk dialog closed', await clickButton(page, /^(close|fechar|cerrar)$/i))
  await sleep(400)

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
