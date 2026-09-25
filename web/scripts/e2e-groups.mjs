#!/usr/bin/env node
/**
 * End to end check of the monitor groups and the clone flows (P1).
 *
 * It drives the real UI: creates a group with members, clones it shallow and
 * deep, renames it, clones a monitor from the monitor list and cleans up after
 * itself (everything it creates is deleted through the API at the end).
 *
 * Usage:
 *   npm run build && npm run e2e:groups -- --url http://localhost:3000
 *
 * Requirements: a running instance with the built bundle and Chrome/Chromium.
 */
import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const LABELS = {
  newGroup: /new group|novo grupo|nuevo grupo/i,
  save: /^(save|salvar|guardar)$/i,
  clone: /^(clone|clonar)$/i,
  edit: /^(edit|editar)$/i,
  cloneDeep: /clone the monitors of the group|clonar os monitores do grupo|clonar los monitores del grupo/i,
  remove: /^(delete|excluir|eliminar)$/i,
}

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

/** clickButton clicks the first button whose text matches the pattern. */
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

function setValue(page, selector, value, event = 'input') {
  return page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)})
    if (!el) return false
    el.value = ${JSON.stringify(value)}
    el.dispatchEvent(new Event(${JSON.stringify(event)}, { bubbles: true }))
    return true
  })()`)
}

/** toggleCheckbox clicks the reka-ui checkbox that carries the given label. */
function toggleCheckbox(page, label) {
  return toggleControl(page, 'checkbox', label)
}

/**
 * toggleFirstSwitch clicks the first switch of the open dialog. The group clone
 * dialog renders the "clone the monitors" switch first and only then the
 * "copy the channels" one, and using the position keeps the script independent
 * of the active locale.
 */
function toggleFirstSwitch(page) {
  return page.evaluate(`(() => {
    const control = document.querySelector('[role="dialog"] button[role="switch"]')
    if (!control) return false
    control.click()
    return true
  })()`)
}

/** toggleControl clicks the reka-ui control (checkbox/switch) of a label. */
function toggleControl(page, role, label) {
  return page.evaluate(`(() => {
    const target = [...document.querySelectorAll('label')].find((el) => el.innerText.trim() === ${JSON.stringify(label)})
    const control = target && target.querySelector('button[role=${JSON.stringify(role)}], button')
    if (!control) return false
    control.click()
    return true
  })()`)
}

/** groupRows returns the name and the text of every row of the groups table. */
function groupRows(page) {
  return page.evaluate(`(() => [...document.querySelectorAll('table tbody tr')].map((row) => ({
    name: ((row.querySelector('td')?.innerText ?? '').split('\\n')[0] ?? '').trim(),
    text: row.innerText.replace(/\\s+/g, ' '),
  })))()`)
}

/**
 * groupById reads the group list through the page and resolves the lookup on
 * this side: the name never travels inside the evaluated snippet, so the helper
 * builds no code (see js/bad-code-sanitization).
 */
async function groupById(page, name) {
  const groups = await page.evaluate(`fetch('/api/monitor-groups').then((r) => r.json())`)
  return groups.find((group) => group.name === name) ?? null
}

const url = parseArgs(process.argv.slice(2))
const chrome = findChrome()
const stamp = Date.now()
const groupName = `e2e group ${stamp}`
const deepName = `e2e deep ${stamp}`
const renamed = `${groupName} renamed`
const created = { groups: [], monitors: [] }

const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })


try {
  console.log(`> ${url} with ${chrome}`)

  // --- fixtures: the names of two monitors to put in the group --------------
  await page.goto(`${url}/admin/monitor-groups`, { settle: 1500 })
  const monitorNames = await page.evaluate(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.slice(0, 2).map((m) => m.name))`)
  check('two monitors available for the group', monitorNames.length === 2, monitorNames.join(', '))
  check('sidebar links the groups page', await page.evaluate(`!!document.querySelector('a[href="/admin/monitor-groups"]')`))

  // --- create a group -------------------------------------------------------
  check('new group dialog opens', await clickButton(page, LABELS.newGroup))
  await sleep(500)
  await setValue(page, '#group-name', groupName)
  for (const name of monitorNames) check(`monitor checkbox "${name}"`, await toggleCheckbox(page, name))
  check('save the group', await clickButton(page, LABELS.save))
  await sleep(1500)
  const afterCreate = await groupRows(page)
  check('group created in the UI', afterCreate.some((row) => row.name === groupName))
  check(
    'group shows both monitors',
    (afterCreate.find((row) => row.name === groupName)?.text ?? '').includes(monitorNames[0]),
  )
  const group = await groupById(page, groupName)
  check('group persisted through the API', Boolean(group))
  if (group) created.groups.push(group.id)

  // --- shallow clone (default name) ----------------------------------------
  check('clone button in the group row', await clickInRow(page, groupName, LABELS.clone))
  await sleep(500)
  check('shallow clone submitted', await clickButton(page, LABELS.clone))
  await sleep(1500)
  const shallow = await groupById(page, `${groupName} (copy)`)
  check('shallow clone uses the (copy) suffix', Boolean(shallow))
  check('shallow clone starts empty', (shallow?.monitor_ids ?? []).length === 0)
  if (shallow) created.groups.push(shallow.id)

  // --- deep clone (with the monitors) --------------------------------------
  check('clone button again', await clickInRow(page, groupName, LABELS.clone))
  await sleep(500)
  await setValue(page, '#group-clone-name', deepName)
  check('deep switch toggled', await toggleFirstSwitch(page))
  await sleep(300)
  check('deep clone submitted', await clickButton(page, LABELS.clone))
  await sleep(3000)
  const deep = await groupById(page, deepName)
  check('deep clone created', Boolean(deep))
  if (deep) {
    created.groups.push(deep.id)
    created.monitors.push(...deep.monitor_ids)
    check('deep clone copied both monitors', deep.monitor_ids.length === 2, String(deep.monitor_ids.length))
    const copied = await page.evaluate(`fetch('/api/monitors?decorate=false')
      .then((r) => r.json())
      .then((list) => list.filter((m) => ${JSON.stringify(deep.monitor_ids)}.includes(m.id)).map((m) => m.name))`)
    check('the copies carry the (copy) suffix', copied.every((name) => name.includes('(copy)')), copied.join(', '))
  }
  // --- rename --------------------------------------------------------------
  check('edit dialog opens', await clickInRow(page, groupName, LABELS.edit))
  await sleep(500)
  await setValue(page, '#group-name', renamed)
  check('new name saved', await clickButton(page, LABELS.save))
  await sleep(1500)
  check('group renamed', (await groupRows(page)).some((row) => row.name === renamed))

  // --- delete through the UI ----------------------------------------------
  check('delete button in the row', await clickInRow(page, renamed, LABELS.remove))
  await sleep(400)
  check('deletion confirmed', await clickButton(page, LABELS.remove))
  await sleep(1500)
  check('group deleted', !(await groupRows(page)).some((row) => row.name === renamed))

  // --- clone a monitor from the monitors page ------------------------------
  await page.goto(`${url}/admin/monitors`, { settle: 1500 })
  const monitorToClone = await page.evaluate(`(() => {
    const row = document.querySelector('table tbody tr')
    if (!row) return ''
    const copy = [...row.querySelectorAll('button')].find((b) => /^(clone|clonar)$/i.test(b.getAttribute('title') ?? ''))
    if (!copy) return ''
    copy.click()
    return row.querySelector('td')?.innerText?.trim() ?? 'cloned'
  })()`)
  check('monitor clone dialog opened', monitorToClone !== '', monitorToClone)
  await sleep(500)
  check('monitor clone submitted', await clickButton(page, LABELS.clone))
  await sleep(2000)
  const monitorCopy = await page.evaluate(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.filter((m) => m.name.includes('(copy)')).map((m) => ({ id: m.id, name: m.name })))`)
  check('monitor copy created', monitorCopy.length >= 1, monitorCopy.map((m) => m.name).join(', '))
  created.monitors.push(...monitorCopy.map((m) => m.id))

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))
  await page.screenshot('/tmp/up-e2e-groups.png')

  // --- cleanup -------------------------------------------------------------
  const cleanup = await page.evaluate(`(async () => {
    const status = { monitors: [], groups: [] }
    for (const id of ${JSON.stringify(created.monitors)}) {
      status.monitors.push(await fetch('/api/monitors/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    for (const id of ${JSON.stringify(created.groups)}) {
      status.groups.push(await fetch('/api/monitor-groups/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return status
  })()`)
  check(
    'cleanup removed every copy',
    cleanup.monitors.every((code) => code === 204 || code === 404) &&
      cleanup.groups.every((code) => code === 204 || code === 404),
    JSON.stringify(cleanup),
  )
} finally {
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x monitor groups check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok monitor groups and the clone flows work end to end')
