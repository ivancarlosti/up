#!/usr/bin/env node
/**
 * Boot smoke test of the single page application.
 *
 * The Go binary embeds whatever `npm run build` produced, so a broken bundle
 * ships silently: `curl /` answers 200 even when the application never mounts
 * (that is how a temporal-dead-zone bug in the theme store reached production as
 * a blank page). This script loads the page in headless Chrome/Chromium, fails
 * on any JavaScript error raised while starting up and checks that the
 * application actually rendered something inside #app.
 *
 * Usage:
 *   npm run build && npm run smoke                 # serves ./dist with vite preview
 *   npm run smoke -- --url https://up.example.com  # checks a running instance
 *   npm run smoke -- --expect login-email          # extra assertion on the DOM
 *
 * Requirements: a built bundle (`npm run build`) and a Chrome/Chromium binary
 * (CHROME_BIN overrides the automatic detection).
 */
import { spawn, spawnSync } from 'node:child_process'
import { existsSync, mkdtempSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

const PREVIEW_PORT = Number(process.env.SMOKE_PORT ?? 4173)

/** Chrome candidates, in order, when CHROME_BIN is not set. */
const CHROME_CANDIDATES = [
  'google-chrome',
  'google-chrome-stable',
  'chromium',
  'chromium-browser',
  '/usr/bin/google-chrome',
  '/snap/bin/chromium',
  '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
]

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
  const options = { url: '', expect: '', dumpDom: false }
  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case '--url':
        options.url = argv[++i] ?? ''
        break
      case '--expect':
        options.expect = argv[++i] ?? ''
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

function findChrome() {
  const explicit = process.env.CHROME_BIN
  if (explicit) {
    if (existsSync(explicit)) return explicit
    console.error(`CHROME_BIN points to a missing file: ${explicit}`)
    process.exit(2)
  }
  for (const candidate of CHROME_CANDIDATES) {
    const probe = spawnSync(candidate, ['--version'], { stdio: 'ignore' })
    if (!probe.error) return candidate
  }
  console.error(
    'no Chrome/Chromium binary found; install one or point CHROME_BIN at it\n' +
      `tried: ${CHROME_CANDIDATES.join(', ')}`,
  )
  process.exit(2)
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
 * loadPage runs headless Chrome against the URL and returns the serialised DOM
 * plus the browser console output written to stderr.
 */
function loadPage(chrome, url) {
  const profile = mkdtempSync(join(tmpdir(), 'up-smoke-'))
  const args = [
    '--headless=new',
    '--no-sandbox',
    '--disable-gpu',
    '--disable-dev-shm-usage',
    `--user-data-dir=${profile}`,
    '--virtual-time-budget=10000',
    '--enable-logging=stderr',
    '--v=1',
    '--dump-dom',
    url,
  ]

  return new Promise((resolve) => {
    const child = spawn(chrome, args, { stdio: ['ignore', 'pipe', 'pipe'] })
    let dom = ''
    let logs = ''
    child.stdout.on('data', (chunk) => (dom += chunk))
    child.stderr.on('data', (chunk) => (logs += chunk))
    const timer = setTimeout(() => child.kill('SIGKILL'), 90000)
    child.on('close', () => {
      clearTimeout(timer)
      rmSync(profile, { recursive: true, force: true })
      resolve({ dom, logs })
    })
  })
}

/** consoleErrors extracts the browser console messages that matter. */
function consoleErrors(logs) {
  return logs
    .split('\n')
    .map((line) => line.trim())
    .filter((line) => FAILURE_PATTERNS.some((pattern) => pattern.test(line)))
    .map((line) => line.replace(/^"[^"]*", source: /, ''))
}

/**
 * inspectApp reports whether the application rendered anything.
 *
 * A boot that fails after `app.mount()` leaves the container as
 * `<div id="app" data-v-app=""><!----></div>`, i.e. a blank page with no console
 * error from the mount itself, so the DOM has to be inspected as well.
 */
function inspectApp(dom) {
  const start = dom.indexOf('<div id="app"')
  if (start < 0) return { found: false, empty: true, elements: 0, text: 0 }
  const end = dom.indexOf('</body>', start)
  const region = (end < 0 ? dom.slice(start) : dom.slice(start, end)).replace(/<!--[\s\S]*?-->/g, '')
  return {
    found: true,
    empty: (region.match(/<[a-z]/gi) ?? []).length <= 1 && region.replace(/<[^>]*>/g, '').trim() === '',
    elements: (region.match(/<[a-z]/gi) ?? []).length,
    text: region.replace(/<[^>]*>/g, '').trim().length,
  }
}

async function main() {
  const options = parseArgs(process.argv.slice(2))
  const chrome = findChrome()

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
  const { dom, logs } = await loadPage(chrome, url)
  if (preview) preview.kill('SIGKILL')

  const errors = consoleErrors(logs)
  const app = inspectApp(dom)
  const missing = options.expect ? !dom.includes(options.expect) : false

  if (options.dumpDom) console.log(dom)

  const state = !app.found ? 'not found' : app.empty ? 'rendered nothing' : `rendered (${app.elements} elements, ${app.text} chars)`
  console.log(`  #app          : ${state}`)
  console.log(`  console errors: ${errors.length}`)
  for (const error of errors.slice(0, 10)) console.log(`    - ${error}`)
  if (options.expect) console.log(`  DOM contains "${options.expect}": ${missing ? 'no' : 'yes'}`)

  if (errors.length > 0 || !app.found || app.empty || missing) {
    console.error('x smoke test failed: the application did not boot cleanly')
    process.exit(1)
  }
  console.log('ok smoke test passed')
}

main().catch((error) => {
  console.error(error)
  process.exit(1)
})
