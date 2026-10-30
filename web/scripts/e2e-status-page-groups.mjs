#!/usr/bin/env node
/**
 * End to end check of the monitor groups inside the public status pages (P2).
 *
 * The promise: a page that includes a group renders every monitor of the group,
 * so adding a monitor to the group publishes it on the page without touching the
 * page. The script creates the three monitors, the group, the page and the
 * memberships with the API, checks the rendered page in a real browser, checks
 * that the admin dialog lists the group and publishes a monitor picked in its
 * search box, and finally deletes everything it created. The monitors are created
 * here on purpose: taking the first rows of the monitor list made the check fail
 * on an empty database.
 *
 * Usage:
 *   npm run build && npm run e2e:status-page-groups -- --url http://localhost:3000
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
const groupName = `e2e page group ${stamp}`
const pageTitle = `e2e page ${stamp}`
const slug = `e2e-page-${stamp}`
const created = { monitors: [], groups: [], pages: [] }

const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })

/** api runs a same-origin request from the page (the session cookie is there). */
const api = (expression) => page.evaluate(expression)


try {
  console.log(`> ${url} with ${chrome}`)

  // --- fixtures through the API --------------------------------------------
  // The three monitors are created here: borrowing the first rows of the monitor
  // list made the check fail on a database that has no monitor yet.
  await page.goto(`${url}/admin/status-pages`, { settle: 1500 })
  const monitorNames = [
    `e2e page ${stamp} monitor a`,
    `e2e page ${stamp} monitor b`,
    `e2e page ${stamp} monitor c`,
  ]
  const monitors = await api(`(async () => {
    const made = []
    for (const [index, name] of ${JSON.stringify(monitorNames)}.entries()) {
      const response = await fetch('/api/monitors', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          name,
          type: 'http',
          active: false,
          interval_seconds: 3600,
          timeout_seconds: 10,
          config: { url: 'http://127.0.0.1:8099/${stamp}-' + index, method: 'GET' },
        }),
      })
      const body = await response.json()
      made.push({ status: response.status, id: body.id, name: body.name })
    }
    return made
  })()`)
  created.monitors.push(...monitors.map((monitor) => monitor.id).filter(Boolean))
  check(
    'the three monitors of the fixture were created',
    monitors.length === 3 && monitors.every((monitor) => monitor.status === 201 && monitor.id > 0),
    monitors.map((monitor) => `${monitor.status} ${monitor.name}`).join(', '),
  )

  const group = await api(`fetch('/api/monitor-groups', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: ${JSON.stringify(groupName)}, monitor_ids: [${monitors[0].id}] }),
  }).then((r) => r.json())`)
  check('group created with one monitor', group?.id > 0 && group.monitor_ids.length === 1)
  if (group?.id) created.groups.push(group.id)

  const statusPage = await api(`fetch('/api/status-pages', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      slug: ${JSON.stringify(slug)},
      title: ${JSON.stringify(pageTitle)},
      theme: 'system',
      is_public: true,
      monitor_ids: [],
    }),
  }).then((r) => r.json())`)
  check('status page created', statusPage?.id > 0)
  if (statusPage?.id) created.pages.push(statusPage.id)

  const linked = await api(`fetch('/api/status-pages/${statusPage.id}/groups', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ groups: [{ group_id: ${group.id} }] }),
  }).then((r) => r.json())`)
  check('group linked to the page', Array.isArray(linked) && linked.length === 1)

  // --- the two expiry switches are independent -----------------------------
  // A page carries one switch per badge instead of the single show_expiry of
  // protocol 1-2: turning the certificate on must leave the domain off, in the
  // anonymous payload that decides what a visitor is allowed to see.
  const expiryFlags = await api(`(async () => {
    const current = await fetch('/api/status-pages/${statusPage.id}').then((r) => r.json())
    const saved = await fetch('/api/status-pages/${statusPage.id}', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...current.page, show_cert_expiry: true, show_domain_expiry: false }),
    })
    const anonymous = await fetch('/api/public/status/${slug}').then((r) => r.json())
    return {
      status: saved.status,
      cert: anonymous.show_cert_expiry,
      domain: anonymous.show_domain_expiry,
      domainBadge: anonymous.monitors?.some((monitor) => Boolean(monitor.domain)) ?? false,
      certBadge: anonymous.monitors?.every((monitor) => Boolean(monitor.certificate)) ?? false,
    }
  })()`)
  check('the page was saved with the two switches', expiryFlags.status === 200, `status ${expiryFlags.status}`)
  check('the certificate switch is on', expiryFlags.cert === true, String(expiryFlags.cert))
  check('the domain switch stayed off', expiryFlags.domain === false, String(expiryFlags.domain))
  check(
    'a page that only asks for the certificate publishes no domain',
    expiryFlags.domainBadge === false,
    String(expiryFlags.domainBadge),
  )

  // --- the public page renders the group -----------------------------------
  await page.goto(`${url}/status/${slug}`, { settle: 1500 })
  const firstPass = await api(`({
    text: document.body.innerText,
    headings: [...document.querySelectorAll('h3')].map((h) => h.textContent.trim()),
  })`)
  check('group section rendered', firstPass.headings.includes(groupName), firstPass.headings.join(' | '))
  check('its monitor is listed', firstPass.text.includes(monitors[0].name), monitors[0].name)

  // --- adding a monitor to the group publishes it --------------------------
  const second = monitors[1]
  const added = await api(`fetch('/api/monitor-groups/${group.id}/monitors', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ monitor_ids: [${monitors[0].id}, ${second.id}] }),
  }).then((r) => r.json())`)
  check('monitor added to the group', (added?.monitor_ids ?? []).length === 2)

  await page.goto(`${url}/status/${slug}`, { settle: 1500 })
  const secondPass = await api(`({
    text: document.body.innerText,
    headings: [...document.querySelectorAll('h3')].map((h) => h.textContent.trim()),
    inSection: (() => {
      const section = [...document.querySelectorAll('section')]
        .find((s) => s.querySelector('h3')?.textContent.trim() === ${JSON.stringify(groupName)})
      return section ? section.innerText.includes(${JSON.stringify(second.name)}) : false
    })(),
  })`)
  check('the page shows the new monitor', secondPass.text.includes(second.name), second.name)
  check('it is inside the group section', secondPass.inSection)
  check(
    'the page itself was never edited',
    secondPass.text.includes(monitors[0].name) && secondPass.headings.includes(groupName),
  )

  // --- the admin dialog drives the selection through the picker ------------
  // The dialog no longer lists checkboxes: it offers a filtered picker over the
  // monitors and the groups, plus the two lists of what the page publishes. This
  // checks that the group the page carries is listed, that a monitor of that group
  // is not offered a second time, and that a monitor picked in the box is saved.
  await page.goto(`${url}/admin/status-pages`, { settle: 1500 })
  const dialog = await api(`(async () => {
    const row = [...document.querySelectorAll('table tbody tr')].find(
      (tr) => (tr.querySelector('td')?.innerText ?? '').trim().startsWith(${JSON.stringify(pageTitle)}),
    )
    const button = [...(row?.querySelectorAll('button') ?? [])].find((b) =>
      /^(edit|editar)$/i.test(b.getAttribute('aria-label') ?? '') || /^(edit|editar)$/i.test(b.textContent.trim()),
    )
    if (!button) return { opened: false }
    button.click()
    await new Promise((resolve) => setTimeout(resolve, 900))
    const dialog = document.querySelector('[role="dialog"]')
    if (!dialog) return { opened: false }
    const groupRow = [...dialog.querySelectorAll('div')].find((el) =>
      (el.innerText ?? '').trim().startsWith(${JSON.stringify(groupName)}),
    )
    const removeGroup = [...(groupRow?.querySelectorAll('button') ?? [])].find((b) =>
      /remove this group from the page|quitar este grupo de la página/i.test(b.getAttribute('aria-label') ?? ''),
    )
    const coveredRow = [...dialog.querySelectorAll('button')].find((b) =>
      (b.innerText ?? '').trim().startsWith(${JSON.stringify(monitors[0].name)}),
    )
    return {
      opened: true,
      hasSearch: Boolean(dialog.querySelector('#status-page-picker')),
      hasGroup: Boolean(groupRow),
      removable: Boolean(removeGroup),
      coveredDisabled: coveredRow?.disabled === true,
      coveredBadge: /via |vía /i.test(coveredRow?.innerText ?? ''),
      text: (dialog.innerText ?? '').slice(0, 400),
    }
  })()`)
  check('admin dialog opens', dialog.opened)
  check('the picker search box is there', dialog.hasSearch)
  check('group listed among the displayed groups', dialog.hasGroup, dialog.hasGroup ? '' : dialog.text)
  check('group can be removed from the page', dialog.removable)
  check('a monitor of the group is not offered twice', dialog.coveredDisabled && dialog.coveredBadge)

  const picked = await api(`(async () => {
    const dialog = document.querySelector('[role="dialog"]')
    const input = dialog?.querySelector('#status-page-picker')
    if (!input) return { clicked: false }
    input.value = ${JSON.stringify(monitors[2].name)}
    input.dispatchEvent(new Event('input', { bubbles: true }))
    await new Promise((resolve) => setTimeout(resolve, 400))
    const offer = [...dialog.querySelectorAll('button')].find((b) =>
      (b.innerText ?? '').trim().startsWith(${JSON.stringify(monitors[2].name)}),
    )
    if (!offer) return { clicked: false, seen: (dialog.innerText ?? '').slice(0, 300) }
    offer.click()
    await new Promise((resolve) => setTimeout(resolve, 300))
    const listed = [...dialog.querySelectorAll('div')].some(
      (el) =>
        (el.innerText ?? '').trim().startsWith(${JSON.stringify(monitors[2].name)}) &&
        [...el.querySelectorAll('button')].some((b) =>
          /remove this monitor from the page|quitar este monitor de la página/i.test(b.getAttribute('aria-label') ?? ''),
        ),
    )
    const save = [...dialog.querySelectorAll('button')].find((b) =>
      /^(save|guardar|salvar)$/i.test(b.textContent.trim()),
    )
    if (!save) return { clicked: true, listed, saved: false }
    save.click()
    await new Promise((resolve) => setTimeout(resolve, 1200))
    const stored = await fetch('/api/status-pages/${statusPage.id}/monitors').then((r) => r.json())
    return { clicked: true, listed, saved: true, stored: stored.map((item) => item.monitor_id) }
  })()`)
  check('monitor picked in the search box', picked.clicked, picked.seen)
  check('it moves to the displayed monitors', picked.listed)
  check(
    'saving stores it on the page',
    picked.saved && (picked.stored ?? []).includes(monitors[2].id),
    JSON.stringify(picked.stored),
  )

  await page.screenshot('/tmp/up-e2e-status-page-groups.png')

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))

  // --- cleanup -------------------------------------------------------------
  const cleanup = await api(`(async () => {
    const status = { pages: [], groups: [], monitors: [] }
    for (const id of ${JSON.stringify(created.pages)}) {
      status.pages.push(await fetch('/api/status-pages/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.groups)}) {
      status.groups.push(await fetch('/api/monitor-groups/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.monitors)}) {
      status.monitors.push(await fetch('/api/monitors/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return status
  })()`)
  check(
    'cleanup removed the page, the group and the monitors',
    [...cleanup.pages, ...cleanup.groups, ...cleanup.monitors].every((code) => code === 204 || code === 404),
    JSON.stringify(cleanup),
  )
} finally {
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x status page groups check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok monitor groups inside the status pages work end to end')
