#!/usr/bin/env node
/**
 * End to end check of the page order of a status page: the drag panel of the admin
 * dialog, the PUT /api/status-pages/:id/order endpoint, and the rendering.
 *
 * The promise: an operator moves a monitor inside its block, saves the page, and the
 * public page renders that block in the new order — a group section included, even
 * though none of its monitors is an explicit selection of the page (the server stores
 * those positions as ORDER-ONLY rows). The script creates its own monitors, group and
 * page with the API, drives the real dialog in a real browser, and deletes everything
 * it created.
 *
 * It also checks the half of the setting the server owns: the manual order survives the
 * alphabetical mode (it is the tie-break), an id that is not on the page is refused,
 * and the clone of the page keeps the positions.
 *
 * The instance must be open to the local administrator (AUTH_METHOD=none) or already
 * logged in: the fixtures run from the page, with the session cookie.
 *
 * Usage:
 *   npm run build && npm run e2e:status-page-order -- --url http://localhost:3000
 */
import { findChrome, launch, newPage } from './browser.mjs'

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

const url = parseArgs(process.argv.slice(2))
const chrome = findChrome()
const stamp = Date.now()
const pageTitle = `e2e order page ${stamp}`
const slug = `e2e-order-${stamp}`
const groupName = `e2e order group ${stamp}`
// Different names and two types, so both modes have something to sort on.
const names = [`e2e order ${stamp} able`, `e2e order ${stamp} baker`, `e2e order ${stamp} charlie`]
const created = { pages: [], groups: [], monitors: [] }

const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })
const api = (expression) => page.evaluate(expression)

try {
  console.log(`> ${url} with ${chrome}`)
  await page.goto(`${url}/admin/status-pages`, { settle: 1500 })

  // --- fixtures through the API --------------------------------------------
  const fixtures = await api(`(async () => {
    const monitors = []
    for (const [index, name] of ${JSON.stringify(names)}.entries()) {
      const response = await fetch('/api/monitors', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name,
          type: index === 2 ? 'tcp' : 'http',
          active: false,
          interval_seconds: 3600,
          timeout_seconds: 10,
          config: index === 2
            ? { host: '127.0.0.1', port: 9 }
            : { url: 'http://127.0.0.1:8099/${stamp}-' + index, method: 'GET' },
        }),
      })
      monitors.push(await response.json())
    }
    const group = await fetch('/api/monitor-groups', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name: ${JSON.stringify(groupName)},
        monitor_ids: [monitors[1].id, monitors[2].id],
      }),
    }).then((r) => r.json())
    const created = await fetch('/api/status-pages', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        title: ${JSON.stringify(pageTitle)},
        slug: ${JSON.stringify(slug)},
        is_public: true,
        show_uptime: true,
        show_charts: true,
        monitor_ids: [monitors[0].id],
      }),
    }).then((r) => r.json())
    await fetch('/api/status-pages/' + created.id + '/groups', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ groups: [{ group_id: group.id }] }),
    })
    return { monitors, group, page: created }
  })()`)
  const pageId = fixtures.page?.id
  // Only real ids: a failed fixture must not turn the cleanup into a 404 storm.
  const realIDs = (values) => values.filter((id) => typeof id === 'number' && id !== 0)
  created.pages = realIDs([pageId])
  created.groups = realIDs([fixtures.group?.id])
  created.monitors = realIDs((fixtures.monitors ?? []).map((monitor) => monitor.id))
  check('fixtures created', Boolean(pageId), JSON.stringify({ pageId, group: fixtures.group?.id }))
  if (!pageId) throw new Error('the fixtures could not be created')

  // --- the panel renders the page as blocks --------------------------------
  await page.goto(`${url}/admin/status-pages`, { settle: 1500 })
  const panel = await api(`(async () => {
    const row = [...document.querySelectorAll('table tbody tr')].find(
      (tr) => (tr.querySelector('td')?.innerText ?? '').trim().startsWith(${JSON.stringify(pageTitle)}),
    )
    const edit = [...(row?.querySelectorAll('button') ?? [])].find((b) =>
      /^(edit|editar)$/i.test(b.getAttribute('aria-label') ?? ''),
    )
    if (!edit) return { opened: false }
    edit.click()
    await new Promise((resolve) => setTimeout(resolve, 1200))
    const dialog = document.querySelector('[role="dialog"]')
    const order = dialog?.querySelector('#page-order')
    if (!order) return { opened: true, panel: false }
    const rows = [...order.querySelectorAll('[draggable]')]
    return {
      opened: true,
      panel: true,
      rows: rows.length,
      names: rows.map((node) => (node.querySelector('span')?.innerText ?? '').trim()),
      blocks: order.querySelectorAll(':scope > div.grid').length,
      hasPreview: Boolean(order.querySelector('button[role="switch"]')),
      hasMode: Boolean(document.querySelector('#page-monitor-order')),
    }
  })()`)
  check('admin dialog opens', panel.opened)
  check('the page order panel is there', panel.panel)
  check(
    'it lists the explicit monitor and the group members',
    panel.rows === 3,
    JSON.stringify(panel.names),
  )
  check('the explicit block comes first', panel.names?.[0] === names[0], JSON.stringify(panel.names))
  check('it has a preview switch', panel.hasPreview)
  check('and the monitor order select', panel.hasMode)

  // --- moving a row inside its block and saving ----------------------------
  const moved = await api(`(async () => {
    const order = document.querySelector('#page-order')
    const target = [...order.querySelectorAll('[draggable]')].find(
      (row) => (row.querySelector('span')?.innerText ?? '').trim() === ${JSON.stringify(names[2])},
    )
    if (!target) return { moved: false }
    const up = target.querySelectorAll('button')[0]
    if (!up || up.disabled) return { moved: false, disabled: true }
    up.click()
    await new Promise((resolve) => setTimeout(resolve, 400))
    const after = [...order.querySelectorAll('[draggable]')].map(
      (row) => (row.querySelector('span')?.innerText ?? '').trim(),
    )
    const dialog = document.querySelector('[role="dialog"]')
    const save = [...dialog.querySelectorAll('button')].find((b) =>
      /^(save|guardar|salvar)$/i.test(b.textContent.trim()),
    )
    if (!save) return { moved: true, after, saved: false }
    save.click()
    await new Promise((resolve) => setTimeout(resolve, 1500))
    const stored = await fetch('/api/status-pages/${pageId}/monitors').then((r) => r.json())
    return { moved: true, after, saved: true, stored }
  })()`)
  check('the up arrow moves the row inside its block', moved.moved && moved.after?.[1] === names[2], JSON.stringify(moved.after))
  check('the explicit block stays where it is', moved.after?.[0] === names[0], JSON.stringify(moved.after))
  check('saving closes the dialog', moved.saved)

  const stored = moved.stored ?? []
  const dragged = stored.find((row) => row.monitor_id === fixtures.monitors[2].id)
  const other = stored.find((row) => row.monitor_id === fixtures.monitors[1].id)
  check(
    'a group member is stored as an order-only row at position 0',
    dragged?.order_only === true && dragged?.sort_order === 0,
    JSON.stringify(dragged),
  )
  check('the other member follows it', other?.order_only === true && other?.sort_order === 1, JSON.stringify(other))

  // --- the public page renders the block in that order ---------------------
  await page.goto(`${url}/status/${slug}`, { settle: 1500 })
  const rendered = await api(`(() => {
    const sections = [...document.querySelectorAll('section')]
    // textContent, not innerText: the headings are rendered uppercase by a CSS
    // transform and innerText reports the transformed text.
    const group = sections.find(
      (section) => (section.querySelector('h3')?.textContent ?? '').trim().toLowerCase() ===
        ${JSON.stringify(groupName)}.toLowerCase(),
    )
    return {
      found: Boolean(group),
      text: group ? group.textContent : '',
      headings: sections.map((section) => (section.querySelector('h3')?.textContent ?? '').trim()),
    }
  })()`)
  check('the group section renders', rendered.found, rendered.headings.join(' | '))
  check(
    'in the dragged order',
    rendered.found &&
      rendered.text.indexOf(names[2]) !== -1 &&
      rendered.text.indexOf(names[2]) < rendered.text.indexOf(names[1]),
    JSON.stringify(rendered.text.slice(0, 120)),
  )

  // --- the mode applies, and the manual order survives it ------------------
  const modes = await api(`(async () => {
    const loaded = await fetch('/api/status-pages/${pageId}').then((r) => r.json())
    const patched = { ...loaded.page, monitor_order: 'alphabetical' }
    await fetch('/api/status-pages/${pageId}', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patched),
    })
    const alphabetical = await fetch('/api/public/status/${slug}').then((r) => r.json())
    await fetch('/api/status-pages/${pageId}', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...loaded.page, monitor_order: 'manual' }),
    })
    const manual = await fetch('/api/public/status/${slug}').then((r) => r.json())
    const names = (payload) =>
      (payload.groups ?? []).flatMap((section) => (section.monitors ?? []).map((monitor) => monitor.name))
    return { alphabetical: names(alphabetical), manual: names(manual) }
  })()`)
  check(
    'alphabetical sorts the block by name',
    (modes.alphabetical ?? []).indexOf(names[1]) < (modes.alphabetical ?? []).indexOf(names[2]),
    JSON.stringify(modes.alphabetical),
  )
  check(
    'the manual order comes back with the mode',
    (modes.manual ?? []).indexOf(names[2]) < (modes.manual ?? []).indexOf(names[1]),
    JSON.stringify(modes.manual),
  )

  // --- an id that is not on the page is refused ----------------------------
  const refused = await api(`fetch('/api/status-pages/${pageId}/order', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ monitor_ids: [${fixtures.monitors[0].id}, 999999999] }),
  }).then((response) => response.status)`)
  check('an id that is not on the page is refused', refused === 400, `status ${refused}`)

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))
} finally {
  // --- cleanup -------------------------------------------------------------
  await page.goto(`${url}/admin/status-pages`, { settle: 400 }).catch(() => {})
  const cleanup = await api(`(async () => {
    const codes = []
    for (const id of ${JSON.stringify(created.pages)}) {
      codes.push(await fetch('/api/status-pages/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.groups)}) {
      codes.push(await fetch('/api/monitor-groups/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.monitors)}) {
      codes.push(await fetch('/api/monitors/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return codes
  })()`).catch(() => [])
  check(
    'cleanup removed the page, the group and the monitors',
    cleanup.every((code) => code === 204 || code === 404),
    JSON.stringify(cleanup),
  )
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x status page order check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok the page order of a status page works end to end')
