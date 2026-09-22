#!/usr/bin/env node
/**
 * Boot smoke test of the single page application.
 *
 * The Go binary embeds whatever `npm run build` produced, so a broken bundle
 * ships silently: `curl /` answers 200 even when the application never mounts
 * (that is how a temporal-dead-zone bug in the theme store reached production as
 * a blank page). This script loads the page in headless Chrome through the CDP
 * driver of `browser.mjs`, fails on any JavaScript error raised while starting
 * up and checks that the application actually rendered something inside #app.
 * It can also assert the real time badge, which is how the "Reconnecting…"
 * regression (the socket was closed when leaving the dashboard) is caught.
 *
 * Usage:
 *   npm run build && npm run smoke                 # serves ./dist with vite preview
 *   npm run smoke -- --url https://up.example.com  # checks a running instance
 *   npm run smoke -- --expect login-email          # extra assertion on the DOM
 *   npm run smoke -- --expect-badge Live           # asserts the realtime badge
 *
 * Requirements: a built bundle (`npm run build`) and a Chrome/Chromium binary
 * (CHROME_BIN overrides the automatic detection).
 */
import { spawn } from 'node:child_process'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { findChrome, launch, newPage } from './browser.mjs'

const PREVIEW_PORT = Number(process.env.SMOKE_PORT ?? 4173)

/** Console patterns that mean "the application did not start". */
const FAILURE_PATTERNS = [
  /\[up\] unexpected error/,
  /\[up\] failed to start/,
  /\[up\] navigation failed/,
  /\[up\] unhandled rejection/,
  /\bReferenceError\b/,
  /\bTypeError\b/,
  /\bSyntaxError\b/,
  /\bUncaught\b/,
  /Refused to apply style/,
]

function parseArgs(argv) {
  const options = { url: '', expect: '', badge: '', dumpDom: false }
  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case '--url':
        options.url = argv[++i] ?? ''
        break
      case '--expect':
        options.expect = argv[++i] ?? ''
        break
      case '--expect-badge':
        options.badge = argv[++i] ?? ''
        break
      case '--dump-dom':
        options.dumpDom = true
        break
      default:
        console.error(`unknown option: ${argv[i]}`)
        process.exit(2)
    }
  }
  return options
}

function findChromeOrExit() {
  try {
    return findChrome()
  } catch (error) {
    console.error(error.message)
    process.exit(2)
  }
}

function wait(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

/** startPreview serves the built dist with vite preview. */
function startPreview() {
  const vite = join('node_modules', '.bin', process.platform === 'win32' ? 'vite.cmd' : 'vite')
  if (!existsSync(vite)) {
    console.error(`${vite} not found: run "npm install" first`)
    process.exit(2)
  }
  const child = spawn(vite, ['preview', '--port', String(PREVIEW_PORT), '--strictPort'], {
    stdio: ['ignore', 'pipe', 'pipe'],
  })
  child.stderr.on('data', (chunk) => {
    // Without a backend the preview proxy logs ECONNREFUSED for every /api/*
    // call; the application handles that (it falls back to the login screen),
    // so the noise is hidden unless SMOKE_VERBOSE is set.
    if (process.env.SMOKE_VERBOSE) process.stderr.write(`[vite] ${chunk}`)
  })
  return child
}

async function waitForServer(url, timeoutMs = 20000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    try {
      const response = await fetch(url, { redirect: 'manual' })
      if (response.status < 500) return true
    } catch {
      /* not listening yet */
    }
    await wait(250)
  }
  return false
}

/**
 * inspect loads the page with the CDP driver and reports what the application
 * rendered inside #app, the boot time, the realtime badge and the console.
 */
async function inspect(chrome, url, options) {
  const browser = await launch({ chrome, width: 1440, height: 900 })
  const page = await newPage(browser, { width: 1440, height: 900 })
  const started = Date.now()
  try {
    await page.goto(url, { settle: 1500 })
    const boot = Date.now() - started
    const app = await page.evaluate(`(() => {
      const root = document.getElementById('app')
      if (!root) return { found: false, elements: 0, text: 0 }
      return {
        found: true,
        elements: root.querySelectorAll('*').length,
        text: (root.innerText || '').trim().length,
      }
    })()`)
    const badge = await page.evaluate(`(() => {
      const el = document.querySelector('header span[title]')
      return el ? el.textContent.trim() : ''
    })()`)
    const html = options.expect ? await page.evaluate('document.documentElement.outerHTML') : ''
    if (options.dumpDom) console.log(await page.evaluate('document.documentElement.outerHTML'))
    return {
      app,
      badge,
      boot,
      html,
      errors: [...page.consoleMessages, ...page.exceptions].filter((line) =>
        FAILURE_PATTERNS.some((pattern) => pattern.test(line)),
      ),
    }
  } finally {
    await page.close()
    browser.close()
  }
}

async function main() {
  const options = parseArgs(process.argv.slice(2))
  const chrome = findChromeOrExit()

  let preview = null
  let url = options.url
  if (!url) {
    if (!existsSync(join('dist', 'index.html'))) {
      console.error('dist/index.html not found: run "npm run build" first')
      process.exit(2)
    }
    preview = startPreview()
    url = `http://localhost:${PREVIEW_PORT}/`
    if (!(await waitForServer(url))) {
      console.error(`vite preview did not answer on ${url}`)
      preview.kill('SIGKILL')
      process.exit(1)
    }
  }

  console.log(`> loading ${url} with ${chrome}`)
  const { app, badge, boot, html, errors } = await inspect(chrome, url, options)
  if (preview) preview.kill('SIGKILL')

  const missing = options.expect ? !html.includes(options.expect) : false
  const wrongBadge = options.badge ? badge !== options.badge : false

  const state = !app.found
    ? 'not found'
    : app.elements <= 1
      ? 'rendered nothing'
      : `rendered (${app.elements} elements, ${app.text} chars)`
  console.log(`  #app          : ${state}`)
  console.log(`  boot          : ${boot} ms`)
  console.log(`  realtime badge: ${badge || '(hidden)'}`)
  console.log(`  console errors: ${errors.length}`)
  for (const error of errors.slice(0, 10)) console.log(`    - ${error}`)
  if (options.expect) console.log(`  DOM contains "${options.expect}": ${missing ? 'no' : 'yes'}`)
  if (options.badge) console.log(`  badge is "${options.badge}": ${wrongBadge ? 'no' : 'yes'}`)

  if (errors.length > 0 || !app.found || app.elements <= 1 || missing || wrongBadge) {
    console.error('x smoke test failed: the application did not boot cleanly')
    process.exit(1)
  }
  console.log('ok smoke test passed')
}

main().catch((error) => {
  console.error(error)
  process.exit(1)
})
