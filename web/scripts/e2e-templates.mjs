#!/usr/bin/env node
/**
 * End to end check of the monitor templates, the bulk importer and the bulk edit.
 *
 * It drives the real UI: creates a template, adds two monitors by pasting
 * "name,url" in the bulk dialog, applies the template to one of them, narrows the
 * template to the monitors of one group (which releases the followers left outside
 * it) and cleans up everything it created.
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

/**
 * clickScopeCard selects one of the three scope cards of the link dialog. The
 * radios carry the API kind as their value (`type`, `groups`, `tags`), which is
 * also what the template stores.
 */
function clickScopeCard(page, kind) {
  return page.evaluate(`(() => {
    const radio = document.querySelector(${JSON.stringify(`[role="dialog"] input[type="radio"][value="${kind}"]`)})
    if (!radio) return false
    radio.click()
    return true
  })()`)
}

/** dialogState reads the selected scope card and the checked group of the link dialog. */
function dialogState(page) {
  return page.evaluate(`(() => {
    const dialog = document.querySelector('[role="dialog"]')
    if (!dialog) return null
    const checked = dialog.querySelector('input[type="radio"]:checked')
    const boxes = [...dialog.querySelectorAll('button[role="checkbox"]')].map((box) => box.getAttribute('aria-checked'))
    return { scope: checked?.value ?? '', checked: boxes.filter((value) => value === 'true').length }
  })()`)
}

const url = parseArgs(process.argv.slice(2))
const chrome = findChrome()
const stamp = Date.now()
const templateName = `e2e template ${stamp}`
const monitorNames = [`e2e bulk ${stamp} a`, `e2e bulk ${stamp} b`]
const created = { monitors: [], templates: [], groups: [] }

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
  // The dialog preselects the first template of the list, whatever the database
  // holds: a template left behind by a failed run can be of another type and the
  // pasted rows would be rejected as "invalid port". The operator says which
  // template the rows are for, so does the run.
  await setValue(page, '#bulk-template', String(template?.id ?? ''), 'change')
  await sleep(300)
  const text = `${monitorNames[0]},http://127.0.0.1:8099/${stamp}-a,,,,30\n${monitorNames[1]},http://127.0.0.1:8099/${stamp}-b,,,,30\n`
  await setValue(page, '#bulk-text', text)
  check('preview requested', await clickButton(page, /preview|pré-visualizar|vista previa/i))
  await sleep(1500)
  const preview = await api(`document.querySelector('[role="dialog"]')?.innerText ?? ''`)
  check(
    'preview reports two ready rows',
    /(Ready|Prontas|Listas): 2/.test(preview),
    preview.replace(/\s+/g, ' ').slice(0, 200),
  )
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
  // Same as the bulk dialog: the first template of the list is whichever one the
  // database sorts first, not necessarily the one the run created.
  await setValue(page, '#apply-template', String(template?.id ?? ''), 'change')
  await sleep(200)
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
  // The two rows below are the only ones of the run that are not a URL, so their
  // target is a host and a port: deriving the port from the run stamp keeps them
  // unique, because the importer reports a row whose target already exists (a
  // monitor of an earlier run) as a duplicate instead of creating it.
  const sslPort = 20000 + (stamp % 10000)
  const tcpPort = 30000 + (stamp % 10000)

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
    await setValue(page, '#bulk-text', `${sslMonitorName},127.0.0.1:${sslPort}\n`)
    await sleep(500)
    const sslRow = await api(`document.querySelector('[role="dialog"]')?.innerText ?? ''`)
    check('the ssl row is ready', /(Ready|Prontas|Listas): 1/.test(sslRow), sslRow.replace(/\s+/g, ' ').slice(0, 200))
    check('the ssl monitor is created', await clickButton(page, /^(create|criar|crear)$/i))
    await sleep(1500)
  }

  // The type column of a row overrides the type of its template (the monitor is
  // then not linked to it: a template never describes another probe type).
  await page.goto(`${url}/admin/monitors`, { settle: 1200 })
  check('bulk dialog opens for the override', await clickButton(page, /add in bulk|adicionar em lote|agregar en lote/i))
  await sleep(600)
  await setValue(page, '#bulk-template', String(template?.id ?? ''), 'change')
  await setValue(page, '#bulk-text', `${overrideName},127.0.0.1:${tcpPort},tcp\n`)
  await sleep(500)
  const overrideRow = await api(`document.querySelector('[role="dialog"]')?.innerText ?? ''`)
  check(
    'the tcp override row is ready',
    /(Ready|Prontas|Listas): 1/.test(overrideRow),
    overrideRow.replace(/\s+/g, ' ').slice(0, 200),
  )
  check('the override monitor is created', await clickButton(page, /^(create|criar|crear)$/i))
  await sleep(2000)
  const imported = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => [${JSON.stringify(sslMonitorName)}, ${JSON.stringify(overrideName)}].includes(m.name))
      .map((m) => ({ id: m.id, name: m.name, type: m.type, template_uuid: m.template_uuid, host: m.config.host, port: m.config.port })))`)
  const sslMonitor = imported.find((m) => m.name === sslMonitorName)
  const overrideMonitor = imported.find((m) => m.name === overrideName)
  check(
    'the ssl monitor was imported',
    sslMonitor?.type === 'ssl' && Number(sslMonitor?.port) === sslPort,
    JSON.stringify(sslMonitor),
  )
  check('the ssl monitor follows its template', Boolean(sslMonitor?.template_uuid), String(sslMonitor?.template_uuid))
  check(
    'the overridden monitor was imported',
    overrideMonitor?.type === 'tcp' && Number(overrideMonitor?.port) === tcpPort,
    JSON.stringify(overrideMonitor),
  )
  check('an overridden row is not linked to the template', !overrideMonitor?.template_uuid, String(overrideMonitor?.template_uuid))
  created.monitors.push(...imported.map((m) => m.id))
  check('bulk dialog closed', await clickButton(page, /^(close|fechar|cerrar)$/i))
  await sleep(400)

  // --- a group-scoped link run is the scope of the template -----------------
  // Regression guard: linking "only the monitors in the selected groups" used to be
  // purely additive, so a template trimmed to one team's group kept pushing its
  // defaults into the monitors of another and the selection meant nothing. The run
  // now makes the selection the scope of the template and releases the followers
  // left outside it (the dialog warns, the preview counts them).
  const linkGroupName = `e2e link group ${stamp}`
  const scopedName = `e2e scoped ${stamp}`
  const releasedName = `e2e released ${stamp}`
  const scope = await api(`(async () => {
    const make = async (name) => {
      const response = await fetch('/api/monitors', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name,
          type: 'http',
          active: false,
          interval_seconds: 60,
          timeout_seconds: 10,
          template_uuid: ${JSON.stringify(template?.uuid ?? '')},
          config: { url: 'https://127.0.0.1:8099/${stamp}/' + name.replace(/ /g, '-'), method: 'GET' },
        }),
      })
      return response.json()
    }
    const scoped = await make(${JSON.stringify(scopedName)})
    const released = await make(${JSON.stringify(releasedName)})
    const group = await fetch('/api/monitor-groups', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name: ${JSON.stringify(linkGroupName)}, monitor_ids: [scoped.id] }),
    }).then((r) => r.json())
    return { scoped, released, group }
  })()`)
  check(
    'the scope setup created two followers and a group',
    scope?.scoped?.template_uuid === template?.uuid &&
      scope?.released?.template_uuid === template?.uuid &&
      scope?.group?.monitor_ids?.includes(scope?.scoped?.id),
    JSON.stringify(scope),
  )
  created.monitors.push(scope?.scoped?.id, scope?.released?.id)
  if (scope?.group?.id) created.groups.push(scope.group.id)

  // The two rows as the API sees them, so the assertions can compare the whole
  // row before and after the run instead of guessing the values.
  const scopeIds = [scope?.scoped?.id, scope?.released?.id].filter(Boolean)
  const scopeRows = () => api(`Promise.all([${scopeIds}].map((id) =>
    fetch('/api/monitors/' + id).then((r) => r.json()).then((m) => ({
      id: m.id,
      name: m.name,
      template_uuid: m.template_uuid,
      interval: m.interval_seconds,
      timeout: m.timeout_seconds,
      active: m.active,
      method: (m.config || {}).method,
      url: (m.config || {}).url,
      groups: m.group_name ?? '',
    }))))`)
  await page.goto(`${url}/admin/monitor-templates`, { settle: 1200 })
  check('the link dialog opens', await clickInRow(page, editedName, /link every monitor|vincular/i))
  await sleep(1200)
  const dialogText = () => api(`document.querySelector('[role="dialog"]')?.innerText ?? ''`)
  const allScopeText = await dialogText()
  check('the whole type scope previews a link', allScopeText.length > 0)

  // Step one, the scope the operator starts from: every monitor of the type,
  // which by definition has no outside and therefore releases nobody.
  check('the type scope links in the dialog', await clickButton(page, /^(link and apply|vincular e aplicar|vincular y aplicar)$/i))
  await sleep(2500)
  const allFollowers = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => m.template_uuid === ${JSON.stringify(template?.uuid ?? '')}).map((m) => m.id))`)
  const typeCount = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => m.type === 'http').length)`)
  check(
    'the type scope linked every monitor of the type',
    allFollowers.length === typeCount && typeCount >= 4,
    `${allFollowers.length} of ${typeCount} http monitors follow`,
  )
  // The state the operator sees before narrowing: both new monitors follow the
  // template, and every field of those rows is what the next step compares with.
  const before = await scopeRows()
  check(
    'both new monitors follow the template before the narrowing',
    before.length === 2 && before.every((m) => m.template_uuid === template?.uuid),
    JSON.stringify(before),
  )
  // What the group run must release: every follower of the template outside the
  // group. The count comes from the API and not from a hardcoded 1, because the
  // monitors of the earlier sections follow this template too.
  const expectedReleased = allFollowers.filter((id) => id !== scope?.scoped?.id).length

  // Step two, the operator narrows the link to the monitors of one group. The
  // scope is the radio card whose value is the kind the API stores.
  check('the link dialog reopens', await clickInRow(page, editedName, /link every monitor|vincular/i))
  await sleep(1200)
  check('the groups scope is picked in the link dialog', await clickScopeCard(page, 'groups'))
  await sleep(800)
  const picked = await api(`(() => {
    const label = [...document.querySelectorAll('[role="dialog"] label')]
      .find((el) => el.innerText.trim().startsWith(${JSON.stringify(linkGroupName)}))
    if (!label) return false
    label.querySelector('button[role="checkbox"], button')?.click()
    return true
  })()`)
  check('the group is selected in the link dialog', picked)
  await sleep(1200)
  const groupScopeText = await dialogText()
  check(
    'the group scope words the warning and the preview differently',
    groupScopeText !== allScopeText && groupScopeText.includes(String(expectedReleased)),
    `${JSON.stringify(groupScopeText.slice(0, 80))} vs ${JSON.stringify(allScopeText.slice(0, 80))}`,
  )
  check('the group scope links in the dialog', await clickButton(page, /^(link and apply|vincular e aplicar|vincular y aplicar)$/i))
  await sleep(2500)
  const afterScope = await scopeRows()
  const keptScoped = afterScope.find((m) => m.id === scope?.scoped?.id)
  const releasedBefore = before.find((m) => m.id === scope?.released?.id)
  const releasedRow = afterScope.find((m) => m.id === scope?.released?.id)
  check('the monitor of the group still follows the template', keptScoped?.template_uuid === template?.uuid, JSON.stringify(keptScoped))
  check('the follower outside the group was released', releasedRow?.template_uuid === '', JSON.stringify(releasedRow))
  // The detach is the link and nothing else: the row, its address, its schedule
  // and its state survive, so a released monitor keeps watching what it watched.
  const changed = (from = {}, to = {}) =>
    Object.keys({ ...from, ...to }).filter((key) => from[key] !== to[key])
  check(
    'the released monitor lost the link and nothing else',
    changed(releasedBefore, releasedRow).join() === 'template_uuid',
    JSON.stringify({ before: releasedBefore, after: releasedRow, changed: changed(releasedBefore, releasedRow) }),
  )
  // The link is not a way to repoint a monitor: the address of the follower is
  // its own, the template only ever sets the fields it owns.
  check(
    'the link kept the per monitor address',
    String(keptScoped?.url) === String(before.find((m) => m.id === scope?.scoped?.id)?.url) &&
      String(keptScoped?.url).includes(`/${stamp}/`),
    JSON.stringify({ before: before.find((m) => m.id === scope?.scoped?.id), after: keptScoped }),
  )
  const remaining = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => m.template_uuid === ${JSON.stringify(template?.uuid ?? '')}).map((m) => m.id))`)
  check(
    'only the group still follows the template',
    remaining.length === 1 && remaining[0] === scope?.scoped?.id,
    JSON.stringify(remaining),
  )
  const groupsAfter = await api(`fetch('/api/monitor-groups')
    .then((r) => r.json())
    .then((list) => (list.groups || list).filter((g) => g.id === ${scope?.group?.id}).map((g) => g.monitor_ids))`)
  check(
    'the link kept the monitor in its group',
    (groupsAfter[0] || []).includes(scope?.scoped?.id),
    JSON.stringify(groupsAfter),
  )
  await page.screenshot('/tmp/up-e2e-link-scope.png')

  // --- the scope of the last run is remembered on the template --------------
  // Regression guard: the dialog used to open on "every monitor of this type"
  // whatever the operator had chosen, because the scope lived only in the request.
  // An admin who re-opened the dialog could not tell (and could not re-run) the
  // selection, and the admin table could not show what a template governs.
  const remembered = await api(`fetch('/api/monitor-templates')
    .then((r) => r.json())
    .then((list) => list.find((t) => t.id === ${template.id}) ?? null)`)
  check(
    'the template remembers the scope of the last run',
    remembered?.link_scope?.kind === 'groups' &&
      remembered.link_scope.group_uuids?.length === 1 &&
      remembered.link_scope.group_uuids[0] === scope?.group?.uuid,
    JSON.stringify(remembered?.link_scope),
  )
  check(
    'the stored scope survives an edit that does not carry one',
    await api(`fetch('/api/monitor-templates/${template.id}', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: ${JSON.stringify(JSON.stringify({ name: editedName, description: '', type: 'http', propagate: true, config: { method: 'POST' }, defaults: { ...(edited?.defaults ?? {}), interval_seconds: 300 } }))},
    })
      .then((r) => r.json())
      .then((saved) => saved.link_scope?.kind === 'groups' && saved.link_scope.group_uuids?.[0] === ${JSON.stringify(scope?.group?.uuid ?? '')})`),
  )
  await page.goto(`${url}/admin/monitor-templates`, { settle: 1500 })
  check('the templates table shows the stored scope', (await api(`document.body.innerText`)).includes(linkGroupName))
  check('the link dialog reopens on the stored scope', await clickInRow(page, editedName, /link every monitor|vincular/i))
  await sleep(1200)
  const reopened = await dialogState(page)
  check('the remembered scope pre-selects the groups card', reopened?.scope === 'groups', JSON.stringify(reopened))
  check('the remembered scope pre-checks the group', (reopened?.checked ?? 0) >= 1, JSON.stringify(reopened))
  check('the link dialog closes', await clickButton(page, /^(cancel|cancelar)$/i))
  await sleep(400)

  // --- a tag scope, and the tag vocabulary it reads --------------------------
  // A tag scope selects by WHOLE tag: the monitor named "... similar" carries
  // "<tag>x", which a substring match would pull into the run.
  const tagName = `e2e-tag-${stamp}`
  const taggedName = `e2e tagged ${stamp}`
  const similarName = `e2e tagged similar ${stamp}`
  const tagged = await api(`(async () => {
    const make = async (name, tags) => {
      const response = await fetch('/api/monitors', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name,
          type: 'http',
          active: false,
          interval_seconds: ${(edited?.defaults ?? {}).interval_seconds === 300 ? 300 : 60},
          timeout_seconds: 10,
          tags,
          config: { url: 'https://127.0.0.1:8099/${stamp}/tagged/' + name.split(' ').join('-'), method: 'GET' },
        }),
      })
      return response.json()
    }
    const scoped = await make(${JSON.stringify(taggedName)}, ${JSON.stringify(tagName)})
    const neighbour = await make(${JSON.stringify(similarName)}, ${JSON.stringify(`${tagName}x`)})
    return { scoped, neighbour }
  })()`)
  created.monitors.push(tagged?.scoped?.id, tagged?.neighbour?.id)
  check(
    'the tag setup created a tagged monitor and a look-alike',
    tagged?.scoped?.tags === tagName && tagged?.neighbour?.tags === `${tagName}x`,
    JSON.stringify(tagged),
  )
  const vocabulary = await api(`fetch('/api/monitors/tags?type=http').then((r) => r.json())`)
  const usage = vocabulary.find((item) => item.tag === tagName)
  check('the tag vocabulary reports the tag with its count', usage?.monitors === 1, JSON.stringify(usage))

  await page.goto(`${url}/admin/monitor-templates`, { settle: 1500 })
  check('the link dialog opens for the tag scope', await clickInRow(page, editedName, /link every monitor|vincular/i))
  await sleep(1200)
  check('the tags scope is picked in the link dialog', await clickScopeCard(page, 'tags'))
  await sleep(800)
  // Nothing is selected yet: the dialog asks for a tag instead of previewing.
  const emptyTagText = await dialogText()
  const tagPicked = await api(`(() => {
    const label = [...document.querySelectorAll('[role="dialog"] label')]
      .find((el) => el.innerText.trim().startsWith(${JSON.stringify(tagName)}))
    if (!label) return false
    label.querySelector('button[role="checkbox"]')?.click()
    return true
  })()`)
  check('the tag is selectable in the link dialog', tagPicked)
  await sleep(1500)
  const tagScopeText = await dialogText()
  check('picking the tag refreshes the preview', tagScopeText !== emptyTagText)
  // The counts behind the preview come from the same dry run the API answers:
  // only the monitor carrying the whole tag is in scope.
  const tagPreview = await api(`fetch('/api/monitor-templates/${template.id}/link-all', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ dry_run: true, scope: { kind: 'tags', tags: [${JSON.stringify(tagName)}] } }),
  }).then((r) => r.json())`)
  check(
    'the tag scope covers only the monitor carrying the whole tag',
    tagPreview?.monitors === 1 && tagPreview?.linked === 1 && tagPreview?.scope?.kind === 'tags',
    JSON.stringify(tagPreview),
  )
  check('the tag scope links in the dialog', await clickButton(page, /^(link and apply|vincular e aplicar|vincular y aplicar)$/i))
  await sleep(2500)
  const tagRows = await api(`Promise.all([${JSON.stringify(tagged?.scoped?.id ?? 0)}, ${JSON.stringify(tagged?.neighbour?.id ?? 0)}]
    .map((id) => fetch('/api/monitors/' + id).then((r) => r.json())
      .then((m) => ({ id: m.id, name: m.name, tags: m.tags, template_uuid: m.template_uuid }))))`)
  const tagScopedRow = tagRows.find((row) => row.id === tagged?.scoped?.id)
  const tagNeighbourRow = tagRows.find((row) => row.id === tagged?.neighbour?.id)
  check('the tagged monitor follows the template', tagScopedRow?.template_uuid === template?.uuid, JSON.stringify(tagScopedRow))
  check(
    'the look-alike tag did not pull the neighbour in',
    !tagNeighbourRow?.template_uuid,
    JSON.stringify(tagNeighbourRow),
  )
  check(
    'the tag scope is stored on the template',
    await api(`fetch('/api/monitor-templates')
      .then((r) => r.json())
      .then((list) => {
        const stored = list.find((t) => t.id === ${template.id})
        return stored?.link_scope?.kind === 'tags' && stored.link_scope.tags?.length === 1 && stored.link_scope.tags[0] === ${JSON.stringify(tagName)}
      })`),
  )

  // --- the bulk tag dialog renames a tag everywhere it is used ---------------
  await page.goto(`${url}/`, { settle: 1500 })
  check('the bulk tag dialog opens', await clickButton(page, /bulk tags|etiquetas em lote|etiquetas en lote/i))
  await sleep(800)
  await setValue(page, '#bulk-tags-selection', 'tag', 'change')
  await sleep(600)
  await setValue(page, '#bulk-tags-tag', tagName, 'change')
  await setValue(page, '#bulk-tags-add', 'renamed')
  await setValue(page, '#bulk-tags-remove', tagName)
  await sleep(1500)
  const bulkText = await api(`document.querySelector('[role="dialog"]')?.innerText ?? ''`)
  check('the bulk tag preview shows the one tagged monitor', /Selected: 1|Selecionados: 1|Seleccionados: 1/.test(bulkText), bulkText.replace(/\s+/g, ' ').slice(0, 160))
  check('the bulk tag run applies', await clickButton(page, /^(apply tags|aplicar etiquetas)$/i))
  await sleep(2000)
  const renamedRow = await api(`fetch('/api/monitors/${tagged?.scoped?.id ?? 0}').then((r) => r.json())`)
  check('the rename landed on the tagged monitor', renamedRow?.tags === 'renamed', String(renamedRow?.tags))
  const neighbourAfter = await api(`fetch('/api/monitors/${tagged?.neighbour?.id ?? 0}').then((r) => r.json())`)
  check(
    'the rename left the look-alike alone',
    neighbourAfter?.tags === `${tagName}x`,
    String(neighbourAfter?.tags),
  )
  check('the bulk tag dialog closes', await clickButton(page, /^(close|fechar|cerrar)$/i))
  await sleep(400)


  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))

  // --- cleanup -------------------------------------------------------------
  const cleanup = await api(`(async () => {
    const status = { monitors: [], templates: [], groups: [] }
    for (const id of ${JSON.stringify(created.monitors)}) {
      status.monitors.push(await fetch('/api/monitors/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.templates)}) {
      status.templates.push(await fetch('/api/monitor-templates/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.groups)}) {
      status.groups.push(await fetch('/api/monitor-groups/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return status
  })()`)
  check(
    'cleanup removed everything',
    [...cleanup.monitors, ...cleanup.templates, ...cleanup.groups].every((code) => code === 204 || code === 404),
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
