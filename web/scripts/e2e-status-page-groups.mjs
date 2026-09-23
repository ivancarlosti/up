#!/usr/bin/env node
/**
 * End to end check of the monitor groups inside the public status pages (P2).
 *
 * The promise: a page that includes a group renders every monitor of the group,
 * so adding a monitor to the group publishes it on the page without touching the
 * page. The script creates the group, the page and the memberships with the API,
 * checks the rendered page in a real browser, checks that the admin dialog shows
 * the selection and finally deletes everything it created.
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
const created = { groups: [], pages: [] }

const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })

/** api runs a same-origin request from the page (the session cookie is there). */
const api = (expression) => page.evaluate(expression)


try {
  console.log(`> ${url} with ${chrome}`)

  // --- fixtures through the API --------------------------------------------
  await page.goto(`${url}/admin/status-pages`, { settle: 1500 })
  const monitors = await api(
    `fetch('/api/monitors?decorate=false').then((r) => r.json()).then((list) => list.slice(0, 2))`,
  )
  check('two monitors available', monitors.length === 2, monitors.map((m) => m.name).join(', '))

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

  // --- the admin dialog shows the selection --------------------------------
  await page.goto(`${url}/admin/status-pages`, { settle: 1500 })
  const dialog = await api(`(async () => {
    const button = [...document.querySelectorAll('button')].find((b) => /^(edit|editar)$/i.test(b.textContent.trim()))
    if (!button) return { opened: false }
    button.click()
    await new Promise((resolve) => setTimeout(resolve, 900))
    const labels = [...document.querySelectorAll('label')].map((el) => el.innerText.trim())
    const box = [...document.querySelectorAll('label')]
      .find((el) => el.innerText.trim().startsWith(${JSON.stringify(groupName)}))
      ?.querySelector('button[role="checkbox"]')
    return {
      opened: true,
      hasGroup: labels.some((text) => text.startsWith(${JSON.stringify(groupName)})),
      checked: box?.getAttribute('data-state') === 'checked' || box?.getAttribute('aria-checked') === 'true',
      labels: labels.filter((text) => text.startsWith('e2e')).slice(0, 3),
    }
  })()`)
  check('admin dialog opens', dialog.opened)
  check('group listed in the dialog', dialog.hasGroup)
  check('group checkbox is checked', dialog.checked, JSON.stringify(dialog.labels))
  await page.screenshot('/tmp/up-e2e-status-page-groups.png')

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))

  // --- cleanup -------------------------------------------------------------
  const cleanup = await api(`(async () => {
    const status = { pages: [], groups: [] }
    for (const id of ${JSON.stringify(created.pages)}) {
      status.pages.push(await fetch('/api/status-pages/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.groups)}) {
      status.groups.push(await fetch('/api/monitor-groups/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return status
  })()`)
  check(
    'cleanup removed the page and the group',
    [...cleanup.pages, ...cleanup.groups].every((code) => code === 204 || code === 404),
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
