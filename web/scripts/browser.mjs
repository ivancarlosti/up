/**
 * browser.mjs - minimal Chrome DevTools Protocol driver.
 *
 * Loading a page with `--dump-dom` tells whether the DOM rendered, but not what
 * it looks like, how wide it is or what the console said after an interaction.
 * This helper drives a headless Chrome over CDP (using the WebSocket client that
 * Node 24 provides, so there is no dependency to install) and exposes just what
 * the smoke test and the screenshot script need:
 *
 *   const browser = await launch({ chrome, width: 1440, height: 900 })
 *   const page = await browser.newPage()
 *   await page.goto('http://localhost:3000/')
 *   await page.screenshot('/tmp/dashboard.png')
 *   const overflow = await page.evaluate('document.documentElement.scrollWidth - window.innerWidth')
 *   browser.close()
 */
import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** findChrome locates a Chrome/Chromium binary (CHROME_BIN wins). */
export function findChrome() {
  const explicit = process.env.CHROME_BIN
  if (explicit) {
    if (existsSync(explicit)) return explicit
    throw new Error(`CHROME_BIN points to a missing file: ${explicit}`)
  }
  const candidates = [
    'google-chrome',
    'google-chrome-stable',
    'chromium',
    'chromium-browser',
    '/usr/bin/google-chrome',
    '/snap/bin/chromium',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  ]
  for (const candidate of candidates) {
    if (!spawnSync(candidate, ['--version'], { stdio: 'ignore' }).error) return candidate
  }
  throw new Error(`no Chrome/Chromium binary found; tried: ${candidates.join(', ')}`)
}

async function waitForEndpoint(port, timeoutMs = 15000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const response = await fetch(`http://127.0.0.1:${port}/json/version`)
      const info = await response.json()
      if (info.webSocketDebuggerUrl) return info.webSocketDebuggerUrl
    } catch {
      /* not listening yet */
    }
    await sleep(150)
  }
  throw new Error(`Chrome did not expose a debugging endpoint on port ${port}`)
}

/**
 * launch starts a headless Chrome and returns the browser handle.
 *
 * The debugging port derives from the process id instead of using the fixed
 * 9222: a Chrome left over from a crashed run keeps owning the default port,
 * starting a new one still succeeds by attaching to that instance, and the run
 * then inherits its `localStorage` and its history - a remembered table sort made
 * a check read the state of an earlier run and report the wrong default column.
 * Pass `port` to pin a specific one.
 */
export async function launch({ chrome, width = 1440, height = 900, port = 9223 + (process.pid % 400) }) {
  const profile = mkdtempSync(join(tmpdir(), 'up-browser-'))
  const child = spawn(
    chrome,
    [
      '--headless=new',
      '--no-sandbox',
      '--disable-gpu',
      '--disable-dev-shm-usage',
      '--hide-scrollbars',
      '--no-first-run',
      '--disable-extensions',
      '--disable-background-networking',
      `--user-data-dir=${profile}`,
      `--remote-debugging-port=${port}`,
      `--window-size=${width},${height}`,
      'about:blank',
    ],
    { stdio: ['ignore', 'ignore', 'ignore'] },
  )

  const endpoint = await waitForEndpoint(port)
  const socket = new WebSocket(endpoint)
  const pending = new Map()
  const listeners = new Set()
  let nextId = 1

  await new Promise((resolve, reject) => {
    socket.addEventListener('open', () => resolve())
    socket.addEventListener('error', () => reject(new Error('could not talk to the Chrome debugging endpoint')))
  })

  socket.addEventListener('message', (message) => {
    const payload = JSON.parse(message.data)
    if (payload.id && pending.has(payload.id)) {
      const { resolve, reject } = pending.get(payload.id)
      pending.delete(payload.id)
      payload.error ? reject(new Error(payload.error.message)) : resolve(payload.result)
      return
    }
    if (payload.method) listeners.forEach((listener) => listener(payload))
  })

  function sendRaw(method, params = {}, sessionId) {
    const id = nextId++
    const request = { id, method, params }
    if (sessionId) request.sessionId = sessionId
    socket.send(JSON.stringify(request))
    return new Promise((resolve, reject) => pending.set(id, { resolve, reject }))
  }

  function close() {
    try {
      socket.close()
    } catch {
      /* already closed */
    }
    child.kill('SIGKILL')
    try {
      rmSync(profile, { recursive: true, force: true })
    } catch {
      // Chrome can still be flushing its profile directory when the process is
      // killed, and a half written tree makes rmdir fail with ENOTEMPTY. The
      // directory lives in tmpdir: leaking it is harmless, while throwing here
      // killed the script after every check had already passed.
    }
  }

  return { sendRaw, onEvent: (listener) => listeners.add(listener), close, child }
}

/**
 * newPage creates a target and returns a tiny page API (navigate, evaluate,
 * screenshot, console capture, viewport/media emulation).
 */
export async function newPage(browser, { width = 1440, height = 900, dark = false } = {}) {
  const { targetId } = await browser.sendRaw('Target.createTarget', { url: 'about:blank' })
  const { sessionId } = await browser.sendRaw('Target.attachToTarget', { targetId, flatten: true })

  const send = (method, params = {}) => browser.sendRaw(method, params, sessionId)

  await send('Page.enable')
  await send('Runtime.enable')
  await send('Network.enable')
  await send('Emulation.setDeviceMetricsOverride', {
    width,
    height,
    deviceScaleFactor: 1,
    mobile: width < 600,
  })
  await send('Emulation.setEmulatedMedia', {
    features: [{ name: 'prefers-color-scheme', value: dark ? 'dark' : 'light' }],
  })

  const consoleMessages = []
  const exceptions = []
  browser.onEvent((payload) => {
    if (payload.sessionId !== sessionId) return
    if (payload.method === 'Runtime.consoleAPICalled' && payload.params.type === 'error') {
      consoleMessages.push(payload.params.args.map((arg) => arg.value ?? arg.description ?? '').join(' '))
    }
    if (payload.method === 'Runtime.exceptionThrown') {
      const details = payload.params.exceptionDetails
      exceptions.push(details.exception?.description ?? details.text ?? 'exception')
    }
  })

  let loaded = false
  browser.onEvent((payload) => {
    if (payload.sessionId === sessionId && payload.method === 'Page.loadEventFired') loaded = true
  })

  return {
    sessionId,

    send,

    /** goto navigates and waits for the load event plus a settle delay. */
    async goto(url, { settle = 1200 } = {}) {
      loaded = false
      await send('Page.navigate', { url })
      const deadline = Date.now() + 20000
      while (!loaded && Date.now() < deadline) await sleep(50)
      await sleep(settle)
    },

    /** evaluate runs an expression in the page and returns its value. */
    async evaluate(expression) {
      const result = await send('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true })
      if (result.exceptionDetails) throw new Error(result.exceptionDetails.text)
      return result.result.value
    },

    /** screenshot writes a PNG of the current viewport. */
    async screenshot(path) {
      const { data } = await send('Page.captureScreenshot', { format: 'png' })
      writeFileSync(path, Buffer.from(data, 'base64'))
      return path
    },

    /** viewport resizes the emulated window (for responsive checks). */
    async viewport({ width: w, height: h, dark: isDark = dark }) {
      await send('Emulation.setDeviceMetricsOverride', { width: w, height: h, deviceScaleFactor: 1, mobile: w < 600 })
      await send('Emulation.setEmulatedMedia', {
        features: [{ name: 'prefers-color-scheme', value: isDark ? 'dark' : 'light' }],
      })
      await sleep(400)
    },

    consoleMessages,
    exceptions,

    async close() {
      await browser.sendRaw('Target.closeTarget', { targetId }).catch(() => {})
    },
  }
}
