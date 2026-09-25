#!/usr/bin/env node
/**
 * End to end check of the notification channel dialog (headless Chrome + CDP).
 *
 * Regression test for the webhook form, which shipped broken in three ways at
 * once: the blank channel had no `config.webhook` block (so switching the type to
 * webhook rendered a dialog without URL, method or body fields), the save sent
 * `created_at: ""`, which cannot be decoded into a `time.Time`, and the numeric
 * inputs sent strings (`resend_interval_seconds`, `config.smtp.port`). The API
 * answered `ERR_INVALID_PAYLOAD`, which the UI showed as a bare
 * "Invalid request body".
 *
 * The script creates a channel through the real dialog and deletes it again, so
 * it needs an instance with the built bundle and an empty-ish database. It is
 * intentionally not part of `npm run smoke`: it writes to the database.
 *
 * Usage:
 *   npm run build && npm run e2e:notifications -- --url http://localhost:3000
 *
 * Requirements: a running instance (the Go binary serving the bundle) and
 * Chrome/Chromium (CHROME_BIN overrides the automatic detection).
 */
import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** Text of the buttons and placeholders, in the three locales the UI ships. */
const LABELS = {
  create: /new channel|novo canal|canal nuevo/i,
  save: /^(save|salvar|guardar)$/i,
  add: /^(add|adicionar|agregar)$/i,
  headerName: /^(header|cabeçalho|encabezado)$/i,
  headerValue: /^(value|valor)$/i,
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

function findChromeOrExit() {
  try {
    return findChrome()
  } catch (error) {
    console.error(error.message)
    process.exit(2)
  }
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

/** setValue writes a native value and dispatches the event the component listens to. */

const url = parseArgs(process.argv.slice(2))
const chrome = findChromeOrExit()
const name = `e2e webhook ${Date.now()}`
const endpoint = 'https://hooks.example.com/e2e'

const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })
let createdId = 0

try {
  console.log(`> ${url}/admin/notifications with ${chrome}`)
  await page.goto(`${url}/admin/notifications`, { settle: 1500 })

  check('dialog opens', await clickButton(page, LABELS.create))
  await sleep(500)

  // The type selector is what used to leave the dialog without a single field.
  check('type switched to webhook', await setValue(page, '#channel-type', 'webhook', 'change'))
  await sleep(500)
  const fields = await page.evaluate(`({
    url: !!document.getElementById('webhook-url'),
    method: !!document.getElementById('webhook-method'),
    body: !!document.getElementById('webhook-body'),
  })`)
  check('webhook fields rendered', fields.url && fields.method && fields.body, JSON.stringify(fields))

  // The same selector drives the chat channels: every type must render its own
  // block (a missing one is exactly the regression this script guards).
  const chatFields = await page.evaluate(`(async () => {
    const select = document.getElementById('channel-type')
    const set = (value) => {
      select.value = value
      select.dispatchEvent(new Event('change', { bubbles: true }))
    }
    const wait = () => new Promise((resolve) => setTimeout(resolve, 250))
    const read = () => ({
      slackToken: !!document.getElementById('slack-token'),
      slackChannel: !!document.getElementById('slack-channel'),
      discordId: !!document.getElementById('discord-id'),
      discordToken: !!document.getElementById('discord-token'),
      telegramToken: !!document.getElementById('telegram-token'),
      telegramChats: !!document.getElementById('telegram-chats'),
      telegramParseMode: !!document.getElementById('telegram-parse-mode'),
    })
    set('slack'); await wait()
    const slack = read()
    set('discord'); await wait()
    const discord = read()
    set('telegram'); await wait()
    const telegram = read()
    set('webhook'); await wait()
    return { slack, discord, telegram, back: !!document.getElementById('webhook-url') }
  })()`)
  check('slack fields rendered', chatFields.slack.slackToken && chatFields.slack.slackChannel, JSON.stringify(chatFields.slack))
  check('discord fields rendered', chatFields.discord.discordId && chatFields.discord.discordToken, JSON.stringify(chatFields.discord))
  check(
    'telegram fields rendered',
    chatFields.telegram.telegramToken && chatFields.telegram.telegramChats && chatFields.telegram.telegramParseMode,
    JSON.stringify(chatFields.telegram),
  )
  check('type switched back to webhook', chatFields.back)

  await setValue(page, '#channel-name', name)
  await setValue(page, '#webhook-url', endpoint)
  check('header row added', await clickButton(page, LABELS.add))
  await sleep(400)

  const headerFilled = await page.evaluate(`(() => {
    const inputs = [...document.querySelectorAll('input')]
    const key = inputs.find((el) => ${LABELS.headerName}.test(el.placeholder))
    const value = inputs.find((el) => ${LABELS.headerValue}.test(el.placeholder))
    if (!key || !value) return false
    key.value = 'Authorization'
    key.dispatchEvent(new Event('input', { bubbles: true }))
    value.value = 'Bearer e2e-token'
    value.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  })()`)
  check('header row filled', headerFilled)

  await page.screenshot('/tmp/up-e2e-notifications-dialog.png')
  check('save clicked', await clickButton(page, LABELS.save))
  await sleep(2000)

  const listed = await page.evaluate(`document.body.innerText.includes(${JSON.stringify(endpoint)})`)
  check('dialog closed after save', !(await page.evaluate(`!!document.getElementById('webhook-url')`)))
  check('channel listed in the page', listed)

  // The stored configuration must match what the dialog showed.
  const stored = await page.evaluate(`fetch('/api/notifications')
    .then((response) => response.json())
    .then((list) => {
      const channel = list.find((item) => item.name === ${JSON.stringify(name)})
      if (!channel) return { found: false }
      return {
        found: true,
        id: channel.id,
        url: channel.config.webhook?.url ?? '',
        headers: channel.config.webhook?.headers ?? [],
        body: (channel.config.webhook?.body_template ?? '').includes('{{.Event}}'),
      }
    })`)
  if (stored.found) createdId = stored.id
  check('channel persisted', stored.found)
  check('url stored', stored.url === endpoint, stored.url ?? '')
  check(
    'header stored',
    (stored.headers ?? []).some((header) => header.key === 'Authorization' && header.value === 'Bearer e2e-token'),
  )
  check('default body template applied', Boolean(stored.body))

  // Editing an existing channel must preload the webhook block as well.
  const edited = await page.evaluate(`(() => {
    const isEdit = (button) => /^(edit|editar)$/i.test(button.textContent.trim())
    const label = [...document.querySelectorAll('h2')].find((el) => el.textContent.trim() === ${JSON.stringify(name)})
    if (!label) return false
    // Walk up to the narrowest ancestor that holds both the name and its button.
    let card = label
    while (card && ![...card.querySelectorAll('button')].some(isEdit)) card = card.parentElement
    const button = card && [...card.querySelectorAll('button')].find(isEdit)
    if (!button) return false
    button.click()
    return true
  })()`)
  check('edit dialog opened', edited)
  await sleep(600)
  const prefilled = await page.evaluate(`document.getElementById('webhook-url')?.value ?? '(no field)'`)
  check('url prefilled on edit', prefilled === endpoint, prefilled)

  await setValue(page, '#webhook-url', `${endpoint}-edited`)
  check('save clicked (edit)', await clickButton(page, LABELS.save))
  await sleep(1500)
  const updated = await page.evaluate(`fetch('/api/notifications/${createdId}')
    .then((response) => response.json())
    .then((channel) => channel.config.webhook?.url ?? '')`)
  check('edited url stored', updated === `${endpoint}-edited`, updated)

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))
  await page.screenshot('/tmp/up-e2e-notifications-saved.png')
} finally {
  if (createdId > 0) {
    const status = await page
      .evaluate(`fetch('/api/notifications/${createdId}', { method: 'DELETE' }).then((response) => response.status)`)
      .catch(() => 0)
    console.log(`  -- cleanup: channel ${createdId} deleted (${status})`)
  }
  await page.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x notification dialog check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok the notification dialog creates a webhook channel end to end')

function setValue(page, selector, value, event = 'input') {
  return page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)})
    if (!el) return false
    el.value = ${JSON.stringify(value)}
    el.dispatchEvent(new Event(${JSON.stringify(event)}, { bubbles: true }))
    return true
  })()`)
}
