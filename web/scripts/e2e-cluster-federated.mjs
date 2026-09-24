#!/usr/bin/env node
/**
 * End to end check of a FEDERATED cluster (one database per node, phase 6 of
 * docs/clustering-federated.md).
 *
 * Unlike every other e2e script this one needs TWO running nodes: in federated
 * mode the configuration travels over the signed peer API, so a single instance
 * can only prove that the peer surface refuses an unsigned caller. Node A is the
 * one under `--url`, node B the one under `--peer-url`. Both must run with
 * CLUSTER_MODE=federated and CLUSTER_PEER_API=true, share CLUSTER_KEY, and have
 * joined each other (docs/development.md, "Two node cluster in three commands").
 *
 * The invariants, in the order they are checked:
 *
 *   1. both nodes report the federated mode and see each other ONLINE in their
 *      registry — the join registered the peer and the ping loop keeps it fresh;
 *   2. an UNSIGNED peer call is refused: the cluster key is the only guard on the
 *      peer API, and CI should notice the day that stops being true;
 *   3. a monitor created on A arrives on B, and an edit made on B converges back
 *      on A — the synchronisation is bidirectional, not a one way mirror;
 *   4. the entity keeps its `uuid` on both nodes while its `revision` advances:
 *      identity is the key, never the local row id;
 *   5. a deletion on A reaches B and does not resurrect (the tombstone property);
 *   6. the presentation settings agree on both nodes.
 *
 * Usage:
 *   npm run build && npm run e2e:cluster-federated -- \
 *     --url http://localhost:3000 --peer-url http://localhost:3002
 */
import { findChrome, launch, newPage } from './browser.mjs'

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

function parseArgs(argv) {
  const args = {
    url: process.env.SMOKE_BASE_URL ?? 'http://localhost:3000',
    peerUrl: process.env.SMOKE_PEER_URL ?? 'http://localhost:3002',
    email: process.env.SMOKE_EMAIL ?? 'admin@example.com',
    password: process.env.SMOKE_PASSWORD ?? 'admin123',
    // How long a change may take to appear on the other node. It has to cover a
    // full pull interval (CLUSTER_SYNC_SECONDS, 15 by default) plus the request,
    // because the push flag is off by default and a node behind NAT is the normal
    // deployment.
    settle: 30000,
  }
  for (let i = 0; i < argv.length; i++) {
    switch (argv[i]) {
      case '--url': args.url = argv[++i] ?? args.url; break
      case '--peer-url': args.peerUrl = argv[++i] ?? args.peerUrl; break
      case '--email': args.email = argv[++i] ?? args.email; break
      case '--password': args.password = argv[++i] ?? args.password; break
      case '--settle': args.settle = Number(argv[++i] ?? args.settle); break
      default:
        console.error(`unknown option: ${argv[i]}`)
        process.exit(2)
    }
  }
  return args
}

const failures = []
function check(label, ok, extra = '') {
  console.log(`  ${ok ? 'ok' : 'x '} ${label}${extra ? ` (${extra})` : ''}`)
  if (!ok) failures.push(label)
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/

// api runs a fetch inside the page of one node. It has to be that node's page:
// the session cookie belongs to an origin, so node B's API is unreadable from a
// page served by node A.
const api = (page, expression) => page.evaluate(expression)

// collection unwraps the shapes the collection endpoints have used.
function collection(payload) {
  if (Array.isArray(payload)) return payload
  for (const key of ['monitors', 'items', 'data', 'rows']) {
    if (Array.isArray(payload?.[key])) return payload[key]
  }
  return []
}

async function signIn(page, base, args) {
  await page.goto(`${base}/`, { settle: 800 })
  return api(page, `fetch('/api/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: ${JSON.stringify(JSON.stringify({ email: args.email, password: args.password }))},
  }).then((r) => r.status)`)
}

// monitorsNamed reads the monitors of that node by name, so a row that is still
// travelling simply is not there yet instead of being an error.
const monitorsNamed = (page, name) => api(page, `fetch('/api/monitors?decorate=false')
  .then((r) => (r.ok ? r.json() : []))
  .then((payload) => {
    const rows = Array.isArray(payload) ? payload : (payload?.monitors ?? payload?.items ?? payload?.data ?? [])
    return rows.filter((row) => row.name === ${JSON.stringify(name)})
  })`)

// waitFor polls until fn returns something truthy, so a change that needs a sync
// cycle is not a failure — only a change that never arrives is.
async function waitFor(fn, timeoutMs, stepMs = 1000) {
  const deadline = Date.now() + timeoutMs
  for (;;) {
    const value = await fn()
    if (value) return value
    if (Date.now() >= deadline) return null
    await sleep(stepMs)
  }
}

const args = parseArgs(process.argv.slice(2))
const chrome = findChrome()
const stamp = Date.now()
const name = `e2e federated ${stamp}`
const renamed = `e2e federated ${stamp} edited`
const payload = (monitorName, interval) => JSON.stringify({
  name: monitorName,
  type: 'http',
  active: true,
  interval_seconds: interval,
  timeout_seconds: 10,
  config: { url: 'http://127.0.0.1:8099/' },
})

const browser = await launch({ chrome, width: 1280, height: 900 })
const pageA = await newPage(browser, { width: 1280, height: 900 })
const pageB = await newPage(browser, { width: 1280, height: 900 })
const created = []

try {
  console.log(`> ${args.url} <-> ${args.peerUrl} with ${chrome}`)

  const loginA = await signIn(pageA, args.url, args)
  const loginB = await signIn(pageB, args.peerUrl, args)
  check('both nodes authenticate', loginA === 200 && loginB === 200, `${loginA} / ${loginB}`)
  if (loginA !== 200 || loginB !== 200) {
    throw new Error('cannot continue without two authenticated nodes')
  }

  // --- 1. the mode, and that the two nodes know each other --------------------
  const statusA = await api(pageA, `fetch('/api/cluster/status').then((r) => r.json())`)
  const statusB = await api(pageB, `fetch('/api/cluster/status').then((r) => r.json())`)
  check('node A runs in federated mode', statusA?.mode === 'federated' && statusA?.enabled === true, String(statusA?.mode))
  check('node B runs in federated mode', statusB?.mode === 'federated' && statusB?.enabled === true, String(statusB?.mode))
  check('the two endpoints are two different nodes',
    Boolean(statusA?.node_id) && statusA.node_id !== statusB?.node_id,
    `${statusA?.node_id} / ${statusB?.node_id}`)

  const peersA = (statusA?.nodes ?? []).filter((node) => node.node_id !== statusA?.node_id)
  const peersB = (statusB?.nodes ?? []).filter((node) => node.node_id !== statusB?.node_id)
  check('each node has the other in its registry', peersA.length > 0 && peersB.length > 0,
    `A:${peersA.map((n) => n.node_id).join(',')} B:${peersB.map((n) => n.node_id).join(',')}`)
  // Only the peer named by --peer-url is asserted to be online. A rig may hold more
  // nodes than the two this script is given, and one of those can legitimately be
  // down — it is then swept offline after the grace period, which is the behaviour
  // the cluster relies on, not a failure of the peer under test.
  const peerHost = new URL(args.peerUrl).host
  const peerOf = (status) => (status?.nodes ?? []).find(
    (node) => node.node_id !== status?.node_id && String(node.api_url ?? '').includes(peerHost))
  const peerA = peerOf(statusA)
  const peerB = peerOf(statusB)
  check('the node at --peer-url is online in both registries',
    peerA?.status === 'online' && peerB?.status === 'online',
    `${peerA?.node_id}:${peerA?.status} / ${peerB?.node_id}:${peerB?.status}`)

  // --- 2. the peer API refuses an unsigned caller -----------------------------
  const unsigned = await api(pageA, `Promise.all([
    fetch('/api/cluster/sync/ping').then((r) => r.status),
    fetch('/api/cluster/sync/changes?cursor=0').then((r) => r.status),
    fetch('/api/cluster/sync/settings').then((r) => r.status),
  ])`)
  check('an unsigned peer call is refused', unsigned.every((code) => code === 401 || code === 403),
    JSON.stringify(unsigned))

  // --- 3. create on A, see it on B --------------------------------------------
  const createdRow = await api(pageA, `fetch('/api/monitors', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: ${JSON.stringify(payload(name, 60))},
  }).then((r) => (r.ok ? r.json() : { status: r.status }))`)
  if (createdRow?.id) created.push(createdRow.id)
  check('a monitor was created on node A', Boolean(createdRow?.id), String(createdRow?.id ?? createdRow?.status))

  const onA = await monitorsNamed(pageA, name)
  check('the monitor is listed on node A', onA.length === 1, String(onA.length))
  check('it has a well formed uuid on node A',
    Boolean(onA[0]?.uuid) && UUID_RE.test(onA[0].uuid), String(onA[0]?.uuid))

  const onB = await waitFor(
    () => monitorsNamed(pageB, name).then((rows) => (rows.length ? rows : null)), args.settle)
  check('the monitor reached node B', Array.isArray(onB),
    onB ? `${onB.length} row(s)` : `not within ${args.settle} ms`)
  check('the identity travelled with it (same uuid)',
    Boolean(onB?.[0]?.uuid) && onB[0].uuid === onA[0]?.uuid, `${onA[0]?.uuid} -> ${onB?.[0]?.uuid}`)

  // --- 4. edit on B, converge back on A ---------------------------------------
  const idOnB = onB?.[0]?.id
  if (idOnB) {
    const editStatus = await api(pageB, `fetch('/api/monitors/${idOnB}', {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: ${JSON.stringify(payload(renamed, 90))},
    }).then((r) => r.status)`)
    check('the edit was accepted on node B', editStatus === 200 || editStatus === 204, String(editStatus))
  } else {
    check('the edit was accepted on node B', false, 'no row on node B to edit')
  }

  const backOnA = await waitFor(async () => {
    const rows = await monitorsNamed(pageA, renamed)
    return rows.length === 1 ? rows : null
  }, args.settle)
  check('the edit converged back to node A', Array.isArray(backOnA),
    backOnA ? `${backOnA.length} row(s)` : `not within ${args.settle} ms`)
  check('the revision advanced while the uuid stayed the same',
    backOnA?.[0]?.uuid === onA[0]?.uuid && Number(backOnA?.[0]?.revision) > Number(onA[0]?.revision),
    `${onA[0]?.revision} -> ${backOnA?.[0]?.revision}`)
  check('no duplicate row was left behind on node B', (await monitorsNamed(pageB, name)).length === 0)

  // --- 5. delete on A, and the tombstone reaches B ----------------------------
  const deleted = created.length
    ? await api(pageA, `fetch('/api/monitors/${created[0]}', { method: 'DELETE' }).then((r) => r.status)`)
    : 0
  check('the monitor was deleted on node A', deleted === 200 || deleted === 204 || deleted === 404, String(deleted))
  const goneOnB = await waitFor(
    async () => ((await monitorsNamed(pageB, renamed)).length === 0 ? 'gone' : null), args.settle)
  check('the deletion reached node B and did not resurrect', goneOnB === 'gone',
    goneOnB === 'gone' ? 'gone on both nodes' : `still on node B after ${args.settle} ms`)

  // --- 6. the presentation settings agree -------------------------------------
  const settingsA = await api(pageA, `fetch('/api/settings').then((r) => r.json())`)
  const settingsB = await api(pageB, `fetch('/api/settings').then((r) => r.json())`)
  for (const key of ['app_name', 'default_locale', 'default_theme']) {
    check(`the setting "${key}" is the same on both nodes`,
      settingsA?.[key] === settingsB?.[key], `${settingsA?.[key]} / ${settingsB?.[key]}`)
  }
  console.log(`  -- node A: ${statusA?.node_name} (${statusA?.node_id}), node B: ${statusB?.node_name} (${statusB?.node_id})`)

  const consoleErrors = [
    ...pageA.consoleMessages, ...pageA.exceptions, ...pageB.consoleMessages, ...pageB.exceptions,
  ].filter((line) => /\[up\] unexpected error|ReferenceError|SyntaxError|TypeError|Uncaught/.test(line))
  check('no console errors', consoleErrors.length === 0, consoleErrors.slice(0, 2).join(' | '))

  // --- cleanup ----------------------------------------------------------------
  // The deletion above is normally the cleanup; this only runs when it did not get
  // that far, so a failed run does not leave probes behind.
  const leftovers = [...(await monitorsNamed(pageA, name)), ...(await monitorsNamed(pageA, renamed))]
  for (const row of leftovers) {
    await api(pageA, `fetch('/api/monitors/${row.id}', { method: 'DELETE' }).then((r) => r.status)`)
  }
  check('no probe was left behind on node A', leftovers.length === 0, `${leftovers.length} cleaned`)
  await sleep(100)
} finally {
  await pageA.close()
  await pageB.close()
  browser.close()
}

if (failures.length > 0) {
  console.error(`x federated cluster check failed: ${failures.join(', ')}`)
  process.exit(1)
}
console.log('ok two federated nodes agree on identity, propagate both ways, and refuse an unsigned caller')
