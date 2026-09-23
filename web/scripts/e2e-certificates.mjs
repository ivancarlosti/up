#!/usr/bin/env node
/**
 * End to end check of the TLS certificate monitoring (P4).
 *
 * It starts the local tls-lab (a valid and an expired certificate), creates an
 * ssl monitor through the real form with the certificate switches on, checks the
 * validity badge and the API payload, and cleans up everything.
 *
 * Usage:
 *   npm run build && npm run e2e:certificates -- --url http://localhost:3000
 */
import { spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'
import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))
const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), '../..')

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

/** startLab runs the repository helper and waits until it is listening. */
function startLab() {
  // `go run` spawns the compiled binary as a child: detached + a group kill is
  // what keeps a port busy after the suite ends.
  const child = spawn('go', ['run', './tools/tls-lab'], { cwd: repoRoot, detached: true })
  const ready = new Promise((resolveReady) => {
    child.stdout.on('data', (chunk) => {
      const text = String(chunk)
      if (text.includes('tls-lab ready')) resolveReady(true)
    })
    child.stderr.on('data', (chunk) => console.error(`[tls-lab] ${String(chunk).trim()}`))
    setTimeout(() => resolveReady(false), 25000)
  })
  return promise_ready(child, ready)
}

/** promise_ready pairs the child with a stop function that kills its group. */
function promise_ready(child, ready) {
  const stop = () => {
    try {
      process.kill(-child.pid, 'SIGTERM')
    } catch {
      child.kill('SIGTERM')
    }
    setTimeout(() => {
      try {
        process.kill(-child.pid, 'SIGKILL')
      } catch {
        /* already gone */
      }
    }, 1500).unref()
  }
  return { child, ready, stop }
}

const url = parseArgs(process.argv.slice(2))
const chrome = findChrome()
const stamp = Date.now()
const monitorName = `e2e ssl ${stamp}`
const created = { monitors: [] }

const lab = startLab()
if (!(await lab.ready)) {
  console.error('x the tls-lab did not start (is Go installed and the port free?)')
  lab.stop()
  process.exit(1)
}

const browser = await launch({ chrome, width: 1440, height: 1000 })
const page = await newPage(browser, { width: 1440, height: 1000 })
const api = (expression) => page.evaluate(expression)


try {
  console.log(`> ${url} with ${chrome}`)

  await page.goto(`${url}/admin/monitors`, { settle: 1500 })
  check('monitor dialog opens', await clickButton(page, /add monitor|adicionar monitor|agregar monitor/i))
  await sleep(600)
  await setValue(page, '#monitor-name', monitorName)
  await setValue(page, '#monitor-type', 'ssl', 'change')
  await sleep(500)
  check('the ssl target fields appear', await page.evaluate(`!!document.querySelector('#monitor-ssl-host')`))
  await setValue(page, '#monitor-ssl-host', '127.0.0.1')
  await setValue(page, '#monitor-ssl-port', '8443')

  // The certificate switches live in their own section, with an id so the check
  // does not depend on the active locale.
  const toggled = await api(`(() => {
    const section = document.querySelector('#monitor-certificate')
    if (!section) return 0
    const switches = [...section.querySelectorAll('button[role="switch"]')]
    switches[0]?.click()
    switches[1]?.click()
    return switches.length
  })()`)
  check('certificate switches toggled', toggled === 2, String(toggled))
  await sleep(400)
  check('the threshold list field appears', await setValue(page, '#monitor-cert-warn', '30,20,19'))
  check('monitor saved', await clickButton(page, /^(save|salvar|guardar)$/i))
  await sleep(1500)

  const monitor = await api(`fetch('/api/monitors?decorate=false')
    .then((r) => r.json())
    .then((list) => list.find((m) => m.name === ${JSON.stringify(monitorName)}) ?? null)`)
  check(
    'ssl monitor created with the certificate switches',
    Boolean(monitor) && monitor.cert_watch && monitor.cert_notify,
  )
  if (!monitor) throw new Error('the monitor was not created')
  created.monitors.push(monitor.id)
  check('the thresholds were stored verbatim', monitor.cert_warn_days === '30,20,19', monitor.cert_warn_days)

  // The first check reads the certificate: wait for the worker to run.
  await sleep(14000)
  const decorated = await api(`fetch('/api/monitors/' + ${monitor.id}).then((r) => r.json())`)
  check('the certificate was captured', Boolean(decorated.certificate), JSON.stringify(decorated.certificate ?? {}))
  const days = decorated.certificate?.days_left
  check('the remaining validity is right', days === 19 || days === 20, String(days))
  check('the issuer is exposed', String(decorated.certificate?.issuer ?? '').includes('up-lab'), decorated.certificate?.issuer)

  // The badge on the detail view is what an operator looks at.
  await page.goto(`${url}/monitors/${monitor.id}`, { settle: 1500 })
  const badge = await api(
    `[...document.querySelectorAll('span')].map((el) => el.innerText.trim()).filter((text) => /^\\d+\\s*(days|dias|días)$/i.test(text))[0] ?? ''`,
  )
  check('the validity badge is rendered', /19|20/.test(badge), badge)
  await page.screenshot('/tmp/up-e2e-certificate.png')

  const consoleErrors = [...page.consoleMessages, ...page.exceptions].filter((line) =>
    /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line),
  )
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))

  const cleanup = await api(`(async () => {
    const status = []
    for (const id of ${JSON.stringify(created.monitors)}) {
      status.push(await fetch('/api/monitors/' + id, { method: 'DELETE' }).then((r) => r.status))
    }
    return status
  })()`)
  check('cleanup removed the monitor', cleanup.every((code) => code === 204 || code === 404), JSON.stringify(cleanup))
} finally {
  await page.close()
  browser.close()
  lab.stop()
}

if (failures.length > 0) {
  console.error(`x certificates check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok the TLS certificate watching works end to end')
