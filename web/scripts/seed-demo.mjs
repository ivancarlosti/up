#!/usr/bin/env node
/**
 * Demo fixture for the screenshots of the README (and for anything else that
 * needs an instance with data in it).
 *
 * It drives a running instance through the real HTTP API, with the credentials
 * of the local `.env` file, and builds a small but realistic dataset:
 *
 *   * notification channels, monitor templates and monitor groups
 *   * 16 monitors spread over the 5 probe types (http, keyword, tcp, dns, ssl),
 *     with non-green rows on purpose: a dashboard where everything is up
 *     documents nothing
 *   * 24 h of heartbeats and their hourly rollups, so the uptime column and the
 *     heartbeat bars are populated at every zoom level
 *   * one real check per monitor, so the statuses, the latencies and the "last
 *     check" column come from genuine probes of this machine
 *   * two status pages, API tokens and IP rules
 *   * two hardware monitored machines with 24 h of samples, their containers and
 *     a cached S.M.A.R.T. report, so the Systems tabs are not empty either. The
 *     samples are written straight into the history tables (no agent runs here),
 *     while the rows themselves are created through the API
 *   * one run of the expiry job, so the certificates and the domains of the
 *     https targets are read for real (RDAP/WHOIS)
 *
 * Usage:
 *   npm run seed:demo                                     # http://localhost:3000 (the APP_URL of .env)
 *   npm run seed:demo -- --force                          # the data here is disposable
 *   SHOTS_BASE_URL=http://127.0.0.1:3010 npm run seed:demo
 *   npm run shots:readme                                  # the screenshots themselves
 *
 * The default address is the `APP_URL` of the local `.env` on purpose: the
 * screenshots are taken by a browser, and the real time hub refuses an upgrade
 * whose Origin is not that exact URL (cmd/server/app.go passes `cfg.AppURL` to
 * ws.NewHub). A browser on `127.0.0.1:3000` therefore sees the header badge fall
 * back to Offline - the pages still render, only the live updates do not.
 *
 * It needs an **empty** database: the rows are created (never upserted) and the
 * heartbeat history is rebuilt with a global DELETE, so a database that already
 * holds monitors is refused unless `--force` is passed.
 *
 * Requirements: the instance of docs/development.md (AUTH_METHOD=account and
 * that account), the MariaDB container on PATH through `docker exec` for the
 * backfill, and a machine that can reach example.com / example.org / the
 * public resolvers used by the probes.
 */
import { execFileSync } from 'node:child_process'

const BASE = process.env.SHOTS_BASE_URL ?? 'http://localhost:3000'
const EMAIL = process.env.SHOTS_EMAIL ?? 'admin@example.com'
const PASSWORD = process.env.SHOTS_PASSWORD ?? 'admin123'
const DB_CONTAINER = process.env.SHOTS_DB_CONTAINER ?? 'up-shots-db'
const DB_NAME = process.env.SHOTS_DB_NAME ?? 'up'
const DB_USER = process.env.SHOTS_DB_USER ?? 'up'
const DB_PASSWORD = process.env.SHOTS_DB_PASSWORD ?? 'secret'
const NODE_ID = process.env.SHOTS_NODE_ID ?? 'up-node-1'

let cookie = ''

/** sleep waits for the probes that the API retries in the background. */
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms))

/** api performs one authenticated call and returns the parsed JSON body. */
async function api(path, { method = 'GET', body } = {}) {
  const headers = { cookie }
  if (body !== undefined) headers['content-type'] = 'application/json'
  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const setCookies = typeof res.headers.getSetCookie === 'function' ? res.headers.getSetCookie() : []
  if (setCookies.length > 0) {
    cookie = setCookies.map((value) => value.split(';')[0]).join('; ')
  }
  const text = await res.text()
  if (!res.ok) {
    throw new Error(`${method} ${path} -> HTTP ${res.status}: ${text.slice(0, 400)}`)
  }
  return text === '' ? null : JSON.parse(text)
}

/** sql pipes one or more statements into the container's MariaDB client. */
function sql(statements) {
  execFileSync(
    'docker',
    ['exec', '-i', DB_CONTAINER, 'mariadb', '-h', '127.0.0.1', `-u${DB_USER}`, `-p${DB_PASSWORD}`, DB_NAME],
    { input: statements.join('\n') + '\n', stdio: ['pipe', 'inherit', 'inherit'] },
  )
}

// ---------------------------------------------------------------------------
// Deterministic pseudo random numbers: the fixture must look organic but be
// reproducible, so the jitter comes from a small LCG and never from Math.random.
// ---------------------------------------------------------------------------
let lcg = 20260927
function rnd() {
  lcg = (lcg * 1103515245 + 12345) & 0x7fffffff
  return lcg / 0x7fffffff
}
function between(min, max) {
  return Math.round(min + rnd() * (max - min))
}
function utcSQLTime(ms) {
  return new Date(ms).toISOString().slice(0, 19).replace('T', ' ')
}

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

const NOTIFICATIONS = [
  {
    name: 'Ops e-mail',
    type: 'smtp',
    active: true,
    is_default: true,
    config: {
      smtp: {
        host: 'smtp.example.com',
        port: 587,
        username: 'alerts@example.com',
        password: 'change-me',
        from: 'Up <alerts@example.com>',
        to: 'ops@example.com,oncall@example.com',
        secure: false,
        use_html: true,
        subject_prefix: '[Up]',
      },
    },
  },
  {
    name: 'On-call webhook',
    type: 'webhook',
    active: true,
    config: {
      webhook: {
        url: 'https://hooks.example.com/up/oncall',
        method: 'POST',
        content_type: 'application/json',
        headers: [{ key: 'Authorization', value: 'Bearer change-me' }],
        body_template: '',
      },
    },
  },
  {
    name: 'Slack #alerts',
    type: 'slack',
    active: true,
    config: { slack: { token: 'xoxb-0000-replace-me', channel: '#alerts', bot_name: 'Up', icon: ':vertical_traffic_light:' } },
  },
  {
    name: 'Telegram team',
    type: 'telegram',
    active: false,
    config: { telegram: { token: '123456:replace-me', chats: '-1001234567890', parse_mode: 'HTML', disable_notification: false, disable_preview: true } },
  },
]

const TEMPLATES = [
  {
    name: 'Public website',
    description: 'https:// probe of a public site, with the certificate watch turned on',
    type: 'http',
    config: { method: 'GET', encoding: 'json', accepted_status_codes: '200-299', ip_family: 'auto' },
    defaults: { interval_seconds: 60, retries: 2, retries_interval_seconds: 15, timeout_seconds: 15, run_on: 'all', cert_watch: true, cert_notify: true, cert_warn_days: '30,14,7', description: 'Public website' },
  },
  {
    name: 'Keyword check',
    description: 'https:// probe that asserts a keyword is present in the body',
    type: 'keyword',
    config: { method: 'GET', keyword: 'Example Domain', case_sensitive: false, accepted_status_codes: '200-299', ip_family: 'auto' },
    defaults: { interval_seconds: 120, retries: 2, retries_interval_seconds: 15, timeout_seconds: 20, run_on: 'all', cert_watch: true },
  },
  {
    name: 'TCP port',
    description: 'TCP connect probe used by the infrastructure fleet',
    type: 'tcp',
    config: { host: '127.0.0.1', port: 3306, ip_family: 'auto' },
    defaults: { interval_seconds: 60, retries: 1, retries_interval_seconds: 10, timeout_seconds: 10, run_on: 'all' },
  },
  {
    name: 'TLS handshake',
    description: 'TLS handshake + certificate expiry of an edge endpoint',
    type: 'ssl',
    config: { host: 'example.com', port: 443, ip_family: 'auto' },
    defaults: { interval_seconds: 3600, retries: 0, timeout_seconds: 15, run_on: 'all', cert_watch: true, cert_notify: true, cert_warn_days: '45,30,15,7' },
  },
]

const GROUPS = [
  { name: 'Public sites', description: 'Everything a visitor can reach', color: '#3b82f6', sort_order: 1 },
  { name: 'Internal services', description: 'Applications behind the VPN', color: '#8b5cf6', sort_order: 2 },
  { name: 'Infrastructure', description: 'Databases, caches and storage', color: '#0ea5e9', sort_order: 3 },
  { name: 'Networking', description: 'DNS and TLS edge', color: '#14b8a6', sort_order: 4 },
]

// SITE_MONITORS: the probes a visitor can reach. "history" describes the 24 h
// backfilled before the real check runs: healthy | flaky | maintenance |
// down-now | paused (see buildBackfill).
const SITE_MONITORS = [
  {
    name: 'Website', type: 'http', group: 'Public sites', template: 'Public website', tags: 'web,public,critical',
    description: 'Main public website',
    interval_seconds: 60, retries: 2, retries_interval_seconds: 15, timeout_seconds: 15,
    cert_watch: true, cert_notify: true, cert_warn_days: '30,14,7',
    config: { url: 'https://example.com/', method: 'GET', accepted_status_codes: '200-299' },
    history: 'healthy',
  },
  {
    name: 'Website (keyword)', type: 'keyword', group: 'Public sites', template: 'Keyword check', tags: 'web,public',
    description: 'Asserts the landing page still renders its main heading',
    interval_seconds: 120, retries: 2, retries_interval_seconds: 15, timeout_seconds: 20, cert_watch: true,
    config: { url: 'https://example.com/', method: 'GET', keyword: 'Example Domain', accepted_status_codes: '200-299' },
    history: 'healthy',
  },
  {
    name: 'Blog', type: 'http', group: 'Public sites', tags: 'web,public',
    interval_seconds: 300, retries: 2, retries_interval_seconds: 20, timeout_seconds: 20,
    config: { url: 'https://example.org/', method: 'GET', accepted_status_codes: '200-299' },
    history: 'healthy',
  },
  {
    name: 'Documentation', type: 'http', group: 'Public sites', tags: 'web,docs', cert_watch: true, cert_warn_days: '30,7',
    interval_seconds: 300, retries: 2, retries_interval_seconds: 20, timeout_seconds: 20,
    config: { url: 'https://www.iana.org/', method: 'GET', accepted_status_codes: '200-299' },
    history: 'flaky',
  },
  {
    name: 'CDN assets', type: 'http', group: 'Public sites', tags: 'cdn,public',
    interval_seconds: 300, retries: 3, retries_interval_seconds: 10, timeout_seconds: 10,
    config: { url: 'https://example.com/favicon.ico', method: 'HEAD', accepted_status_codes: '200-299' },
    history: 'healthy',
  },
  {
    name: 'Legacy portal', type: 'http', group: 'Public sites', tags: 'web,legacy', active: false,
    description: 'Paused until the migration finishes',
    interval_seconds: 600, retries: 2, retries_interval_seconds: 30, timeout_seconds: 20,
    config: { url: 'https://example.org/', method: 'GET', accepted_status_codes: '200-299' },
    history: 'paused',
  },
]

// PLATFORM_MONITORS: the backend fleet. Two of them are intentionally not
// green, because a dashboard where everything is up documents nothing: the
// Redis cache refuses its port (down) and the mail relay must keep refusing it
// (upside down probe, so the refusal is the healthy answer).
const PLATFORM_MONITORS = [
  {
    name: 'API gateway', type: 'http', group: 'Internal services', tags: 'api,critical',
    interval_seconds: 60, retries: 2, retries_interval_seconds: 10, timeout_seconds: 10,
    config: { url: 'https://example.com/', method: 'GET', accepted_status_codes: '200-299' },
    history: 'healthy',
  },
  {
    name: 'API health endpoint', type: 'keyword', group: 'Internal services', tags: 'api,health',
    interval_seconds: 60, retries: 2, retries_interval_seconds: 10, timeout_seconds: 10,
    config: { url: 'https://example.com/', method: 'GET', keyword: 'Example Domain', accepted_status_codes: '200-299' },
    history: 'flaky',
  },
  {
    name: 'Customer portal', type: 'http', group: 'Internal services', tags: 'app,critical',
    interval_seconds: 120, retries: 2, retries_interval_seconds: 15, timeout_seconds: 15,
    cert_watch: true, cert_notify: true, cert_warn_days: '30,14,7',
    config: { url: 'https://example.org/', method: 'GET', accepted_status_codes: '200-299' },
    history: 'maintenance',
  },
  {
    name: 'Primary database', type: 'tcp', group: 'Infrastructure', template: 'TCP port', tags: 'db,critical',
    description: 'MariaDB primary',
    interval_seconds: 60, retries: 1, retries_interval_seconds: 10, timeout_seconds: 10,
    config: { host: '127.0.0.1', port: 3306 },
    history: 'healthy',
  },
  {
    name: 'Cache cluster (Redis)', type: 'tcp', group: 'Infrastructure', tags: 'cache,critical',
    interval_seconds: 60, retries: 2, retries_interval_seconds: 10, timeout_seconds: 5,
    config: { host: '127.0.0.1', port: 6379 },
    history: 'down-now', downFromMinutes: 47,
  },
  {
    name: 'Object storage API', type: 'tcp', group: 'Infrastructure', tags: 'storage',
    interval_seconds: 300, retries: 1, retries_interval_seconds: 20, timeout_seconds: 10,
    config: { host: 'example.com', port: 443 },
    history: 'healthy',
  },
  {
    name: 'Mail relay (must stay blocked)', type: 'tcp', group: 'Infrastructure', tags: 'security,firewall',
    upside_down: true,
    description: 'Upside down probe: the SMTP port of this host must keep refusing connections',
    interval_seconds: 300, retries: 0, timeout_seconds: 5,
    config: { host: '127.0.0.1', port: 25 },
    history: 'healthy',
  },
  {
    name: 'Public DNS (A record)', type: 'dns', group: 'Networking', tags: 'dns,edge',
    interval_seconds: 120, retries: 2, retries_interval_seconds: 15, timeout_seconds: 10,
    config: { hostname: 'example.com', record_type: 'A', resolver_server: '1.1.1.1', ip_family: 'ipv4' },
    history: 'healthy',
  },
  {
    name: 'Domain delegation (NS)', type: 'dns', group: 'Networking', tags: 'dns,edge',
    interval_seconds: 600, retries: 1, retries_interval_seconds: 30, timeout_seconds: 10,
    config: { hostname: 'example.org', record_type: 'NS', resolver_server: '8.8.8.8' },
    history: 'healthy',
  },
  {
    name: 'Edge TLS certificate', type: 'ssl', group: 'Networking', template: 'TLS handshake', tags: 'tls,edge,critical',
    cert_watch: true, cert_notify: true, cert_warn_days: '45,30,15,7',
    interval_seconds: 3600, retries: 0, timeout_seconds: 15,
    config: { host: 'example.com', port: 443 },
    history: 'healthy',
  },
]

const MONITORS = [...SITE_MONITORS, ...PLATFORM_MONITORS]

// NON_GREEN is the same intent as the comments above, written down where it can
// be checked: the two rows a reader of the dashboard is meant to notice, and the
// status each one has to carry once the fixture has run. `report` below waits
// for them, so a fixture that silently turned them green would say so.
const NON_GREEN = {
  'Cache cluster (Redis)': 'down',
  'Legacy portal': 'maintenance',
}

// ---------------------------------------------------------------------------
// The 24 h of history.
//
// The bars, the uptime column and the latency of the table are computed from
// the heartbeats and from their hourly rollups (services/stats.go), so the
// fixture writes both: the beats first, then the rollup rows rebuilt with the
// same aggregate the service runs on every write.
// ---------------------------------------------------------------------------

const BEAT_STEP_MS = 5 * 60 * 1000
const BEAT_COUNT = 288; // 24 h at one beat every 5 minutes
const LATENCY = { http: [90, 260], keyword: [110, 320], tcp: [6, 26], dns: [9, 38], ssl: [60, 150] }
const FAILURES = {
  http: [
    { code: 502, message: 'HTTP 502 Bad Gateway' },
    { code: 503, message: 'HTTP 503 Service Unavailable' },
    { code: 0, message: 'Get "https://target/": dial tcp: i/o timeout' },
  ],
  keyword: [
    { code: 200, message: 'keyword "Example Domain" was not found in the body' },
    { code: 0, message: 'context deadline exceeded while waiting for the response' },
  ],
  tcp: [
    { code: 0, message: 'dial tcp 127.0.0.1:6379: connect: connection refused' },
    { code: 0, message: 'dial tcp: i/o timeout' },
  ],
  dns: [
    { code: 0, message: 'no such host (NXDOMAIN)' },
    { code: 0, message: 'read udp 127.0.0.1: reading the answer: i/o timeout' },
  ],
  ssl: [{ code: 0, message: 'tls: handshake failure' }],
}

function latencyFor(type) {
  const [min, max] = LATENCY[type] ?? [20, 80]
  return between(min, max)
}

/** buildBackfill returns the SQL that wipes and rebuilds the last 24 h. */
function buildBackfill(monitors) {
  const now = Date.now()
  // The newest synthetic beat stops 6 minutes short of "now": the real check
  // performed at the end of the seeding lands on top of it.
  const lastBeat = Math.floor((now - 6 * 60 * 1000) / BEAT_STEP_MS) * BEAT_STEP_MS
  const firstBeat = lastBeat - (BEAT_COUNT - 1) * BEAT_STEP_MS

  const values = []
  for (const monitor of monitors) {
    for (let index = 0; index < BEAT_COUNT; index++) {
      const at = firstBeat + index * BEAT_STEP_MS
      let status = 1
      if (monitor.history === 'paused' && index > BEAT_COUNT - 37) {
        // Paused three hours ago: the history stops there, exactly like a
        // monitor whose operator switched it off.
        continue
      }
      if (monitor.history === 'down-now' && at >= now - (monitor.downFromMinutes ?? 45) * 60 * 1000) {
        status = 0
      } else if (monitor.history === 'maintenance' && index >= BEAT_COUNT - 34 && index < BEAT_COUNT - 22) {
        status = 3
      } else if (monitor.history === 'flaky' && rnd() < 0.05) {
        status = 0
      } else if (monitor.history === 'healthy' && rnd() < 0.004) {
        status = 0
      }

      let latency = 0
      let code = 0
      let message = ''
      if (status === 1) {
        latency = latencyFor(monitor.type)
        code = monitor.type === 'http' || monitor.type === 'keyword' ? 200 : 0
      } else if (status === 3) {
        code = 0
        message = 'maintenance window'
      } else {
        const failure = FAILURES[monitor.type][Math.floor(rnd() * FAILURES[monitor.type].length)]
        code = failure.code
        message = failure.message
      }
      values.push(`(${monitor.id},'${NODE_ID}',${status},${latency},${code},'${message.replace(/'/g, "''")}',${status === 1 ? 0 : 1},'${utcSQLTime(at)}')`)
    }
  }

  const statements = [
    'DELETE FROM heartbeats;',
    'DELETE FROM heartbeat_rollups;',
    'DELETE FROM monitor_states;',
  ]
  const COLUMNS = 'INSERT INTO heartbeats (monitor_id,node_id,status,latency_ms,status_code,message,important,created_at) VALUES '
  for (let index = 0; index < values.length; index += 200) {
    statements.push(COLUMNS + values.slice(index, index + 200).join(',') + ';')
  }
  // The rollup is the exact reduction of the hour, as documented on
  // models.HeartbeatRollup; "updated_at" is only bookkeeping.
  //
  // REPLACE instead of INSERT: the running scheduler writes its own beats (and
  // their rollup rows) while the fixture runs, and the bucket it already
  // incremented would make a plain INSERT collide on the primary key.
  statements.push(`REPLACE INTO heartbeat_rollups
      (monitor_id,bucket_at,up,down,pending,maintenance,total,up_latency_sum,up_latency_count,latency_min,latency_max,updated_at)
    SELECT monitor_id,
           DATE_FORMAT(created_at, '%Y-%m-%d %H:00:00'),
           SUM(status = 1), SUM(status = 0), SUM(status = 2), SUM(status = 3), COUNT(*),
           SUM(CASE WHEN status = 1 THEN latency_ms ELSE 0 END),
           SUM(CASE WHEN status = 1 THEN 1 ELSE 0 END),
           MIN(latency_ms), MAX(latency_ms), UTC_TIMESTAMP()

    FROM heartbeats
    GROUP BY monitor_id, DATE_FORMAT(created_at, '%Y-%m-%d %H:00:00');`)
  return statements
}

// ---------------------------------------------------------------------------
// The machines of the Systems page.
//
// A real system is a Beszel agent dialling the hub, and the fixture does not run
// one (the README set is taken on a laptop, not on a fleet): the rows are created
// through the API - so the configuration, the token and the identity are the ones
// the dashboard itself would produce - and the samples are written straight into
// the four history tables, the same way the heartbeat backfill below does it.
//
// What the tables then show is the shape of a real report: one minute per sample
// for the last 24 h, container rows at the same cadence, and one cached SMART
// report per machine.
// ---------------------------------------------------------------------------
const SYSTEMS = [
  {
    name: 'Hypervisor (proxmox-01)',
    host: 'proxmox-01.lan',
    identity: {
      hostname: 'proxmox-01',
      os_name: 'Debian GNU/Linux 12 (bookworm)',
      kernel: '6.8.12-4-pve',
      arch: 'amd64',
      cpu_model: 'AMD Ryzen 9 7950X 16-Core Processor',
      cores: 16,
      threads: 32,
      memory_total: 137438953472,
      agent_version: '0.21.1',
      smart_interval: 600,
    },
    shape: { cpu: 22, cpuSwing: 14, mem: 46, memSwing: 6, disk: 61, diskGrowth: 0.4, temp: 44, net: 780000, netSwing: 620000 },
    containers: [
      { name: 'up', image: 'ghcr.io/ivancarlosti/up:latest', status: 'Up 3 days', health: '', ports: '0.0.0.0:3000->3000/tcp', cpu: 1.4, mem_mb: 96, net_sent: 28000, net_recv: 41000 },
      { name: 'traefik', image: 'traefik:v3.1', status: 'Up 3 days', health: 'healthy', ports: '0.0.0.0:80->80/tcp, 0.0.0.0:443->443/tcp', cpu: 0.6, mem_mb: 74, net_sent: 420000, net_recv: 610000 },
      { name: 'mariadb', image: 'mariadb:11', status: 'Up 3 days', health: 'healthy', ports: '3306/tcp', cpu: 3.1, mem_mb: 812, net_sent: 94000, net_recv: 122000 },
      { name: 'grafana', image: 'grafana/grafana:11.2.0', status: 'Up 2 days', health: '', ports: '0.0.0.0:3001->3000/tcp', cpu: 0.9, mem_mb: 148, net_sent: 18000, net_recv: 26000, update_available: true },
      { name: 'node-exporter', image: 'prom/node-exporter:v1.8.2', status: 'Up 3 days', health: '', ports: '0.0.0.0:9100->9100/tcp', cpu: 0.3, mem_mb: 22, net_sent: 12000, net_recv: 9000 },
    ],
    // One disk reads PASSED and one reads FAILED on purpose: the S.M.A.R.T. tab
    // has to show both badges, and the failing attributes are the reason the
    // machine is watched at all.
    smart: {
      sda: {
        model_name: 'Samsung SSD 870 EVO 1TB',
        serial_number: 'S6PTNX0T912345A',
        firmware_version: 'SVT02B6Q',
        capacity: 1000204886016,
        status: 'PASSED',
        disk_name: 'sda',
        disk_type: 'sata',
        temperature: 34,
        attributes: [
          { id: 5, name: 'Reallocated_Sector_Ct', value: 100, worst: 100, threshold: 10, raw_value: 0, raw_string: '0' },
          { id: 9, name: 'Power_On_Hours', value: 97, worst: 97, threshold: 0, raw_value: 18422, raw_string: '18422' },
          { id: 177, name: 'Wear_Leveling_Count', value: 94, worst: 94, threshold: 0, raw_value: 186, raw_string: '186' },
          { id: 190, name: 'Airflow_Temperature_Cel', value: 66, worst: 52, threshold: 0, raw_value: 34, raw_string: '34 (Min/Max 28/41)' },
          { id: 199, name: 'CRC_Error_Count', value: 100, worst: 100, threshold: 0, raw_value: 0, raw_string: '0' },
          { id: 241, name: 'Total_LBAs_Written', value: 99, worst: 99, threshold: 0, raw_value: 31240849736, raw_string: '31240849736' },
        ],
      },
      sdb: {
        model_name: 'WDC WD40EFRX-68N32N0',
        serial_number: 'WD-WCC7K4PN9L2R',
        firmware_version: '82.00A82',
        capacity: 4000787030016,
        status: 'FAILED!',
        disk_name: 'sdb',
        disk_type: 'sata',
        temperature: 41,
        attributes: [
          { id: 5, name: 'Reallocated_Sector_Ct', value: 12, worst: 1, threshold: 140, raw_value: 2041, raw_string: '2041', when_failed: 'now' },
          { id: 9, name: 'Power_On_Hours', value: 61, worst: 61, threshold: 0, raw_value: 34118, raw_string: '34118' },
          { id: 197, name: 'Current_Pending_Sector', value: 0, worst: 0, threshold: 0, raw_value: 48, raw_string: '48', when_failed: 'now' },
          { id: 198, name: 'Offline_Uncorrectable', value: 100, worst: 100, threshold: 0, raw_value: 3, raw_string: '3' },
          { id: 194, name: 'Temperature_Celsius', value: 59, worst: 45, threshold: 0, raw_value: 41, raw_string: '41' },
        ],
      },
      nvme0n1: {
        model_name: 'Samsung SSD 990 PRO 2TB',
        serial_number: 'S7KHNJ0W412345T',
        firmware_version: '4B2QJXD7',
        capacity: 2000398934016,
        status: 'PASSED',
        disk_name: 'nvme0n1',
        disk_type: 'nvme',
        temperature: 47,
        attributes: [
          { id: 5, name: 'Reallocate_NAND_Blk_Cnt', value: 100, worst: 100, threshold: 10, raw_value: 0, raw_string: '0' },
          { id: 9, name: 'Power_On_Hours', value: 99, worst: 99, threshold: 0, raw_value: 9145, raw_string: '9145' },
          { id: 194, name: 'Temperature', value: 53, worst: 41, threshold: 0, raw_value: 47, raw_string: '47 Celsius' },
          { id: 202, name: 'Percent_Lifetime_Remain', value: 98, worst: 98, threshold: 1, raw_value: 98, raw_string: '98' },
          { id: 241, name: 'Total_LBAs_Written', value: 99, worst: 99, threshold: 0, raw_value: 88142090112, raw_string: '88142090112' },
        ],
      },
    },
  },
  {
    name: 'Edge VPS (fra1)',
    host: 'edge-fra1.example.com',
    identity: {
      hostname: 'edge-fra1',
      os_name: 'Ubuntu 24.04.1 LTS',
      kernel: '6.8.0-45-generic',
      arch: 'arm64',
      cpu_model: 'Ampere Altra Q80-30',
      cores: 4,
      threads: 4,
      memory_total: 8589934592,
      agent_version: '0.21.1',
      smart_interval: 600,
    },
    shape: { cpu: 9, cpuSwing: 7, mem: 63, memSwing: 9, disk: 38, diskGrowth: 0.9, temp: 39, net: 2400000, netSwing: 1800000 },
    containers: [
      { name: 'caddy', image: 'caddy:2.8-alpine', status: 'Up 5 days', health: '', ports: '0.0.0.0:443->443/tcp, 0.0.0.0:80->80/tcp', cpu: 0.8, mem_mb: 41, net_sent: 1850000, net_recv: 2300000 },
      { name: 'watchtower', image: 'containrrr/watchtower:1.7.1', status: 'Up 5 days', health: '', ports: '', cpu: 0.1, mem_mb: 18, net_sent: 4200, net_recv: 3100 },
    ],
    // No S.M.A.R.T. at all: a VPS usually has no disk it can read, and the tab
    // has to show that state for this machine without hiding the other one.
    smart: {},
  },
]

// ---------------------------------------------------------------------------
// Orchestration
// ---------------------------------------------------------------------------

const STATUS_PAGES = [
  {
    slug: 'status',
    title: 'Example Cloud status',
    description: 'Live availability of the public services',
    footer_text: 'All times are UTC. Subscribe to updates at ops@example.com.',
    theme: 'system',
    is_public: true,
    show_uptime: true,
    show_charts: true,
    show_tags: true,
    show_cert_expiry: true,
    uptime_window_hours: 24,
    monitors: ['Website', 'Website (keyword)', 'Blog', 'Documentation', 'CDN assets', 'API gateway'],
    groups: [
      { name: 'Public sites', display_name: 'Websites' },
      { name: 'Internal services', display_name: 'Platform' },
    ],
  },
  {
    slug: 'internal',
    title: 'Internal platform status',
    description: 'Draft page: visible to administrators only until the launch',
    footer_text: '',
    theme: 'dark',
    is_public: false,
    show_uptime: true,
    show_charts: true,
    show_tags: false,
    show_cert_expiry: false,
    uptime_window_hours: 168,
    monitors: ['API gateway', 'Primary database', 'Cache cluster (Redis)', 'Object storage API'],
    groups: [{ name: 'Infrastructure', display_name: 'Data layer' }],
  },
]

const TOKENS = [
  { name: 'Grafana dashboard', scopes: ['read'], expires_in_days: 90 },
  { name: 'CI pipeline', scopes: ['read', 'write'], expires_in_days: 30 },
  { name: 'Retired exporter', scopes: ['read'], expires_in_days: 365, revoke: true },
]

// The loopback rules are what keep this machine's session working: a non-empty
// allow list turns the scope into an allow-list (see services/iprule_decision.go),
// so both families have to be in it. "localhost" resolves to ::1 on a machine
// that publishes the IPv6 loopback first, and a browser that connects over IPv6
// is refused by a list that only holds 127.0.0.1/32: the pages then render their
// empty state and the real time badge reads "Offline" while curl, over IPv4,
// sees the whole dataset.
const IP_RULES = [
  { cidr: '127.0.0.1/32', action: 'allow', scope: 'all', note: 'Loopback (local operator)', enabled: true },
  { cidr: '::1/128', action: 'allow', scope: 'all', note: 'Loopback (local operator, IPv6)', enabled: true },
  { cidr: '203.0.113.0/24', action: 'allow', scope: 'dashboard', note: 'Head office', enabled: true },
  { cidr: '2001:db8::/32', action: 'allow', scope: 'api', note: 'CI runners (IPv6)', enabled: true },
  { cidr: '198.51.100.10/32', action: 'deny', scope: 'all', note: 'Abusive scraper', enabled: true },
  { cidr: '192.0.2.0/24', action: 'deny', scope: 'public', note: 'Old CDN range', enabled: false },
]

const DEFAULT_NOTIFICATIONS = ['Ops e-mail', 'On-call webhook', 'Slack #alerts']

async function login() {
  await api('/api/auth/login', { method: 'POST', body: { email: EMAIL, password: PASSWORD } })
}

async function seedNotifications() {
  const ids = new Map()
  for (const channel of NOTIFICATIONS) {
    const created = await api('/api/notifications', { method: 'POST', body: channel })
    ids.set(created.name, created.id)
  }
  console.log(`  notifications: ${ids.size}`)
  return ids
}

async function seedTemplates() {
  const byName = new Map()
  for (const template of TEMPLATES) {
    const created = await api('/api/monitor-templates', { method: 'POST', body: template })
    byName.set(created.name, created)
  }
  console.log(`  monitor templates: ${byName.size}`)
  return byName
}

async function seedGroups() {
  const byName = new Map()
  for (const group of GROUPS) {
    const created = await api('/api/monitor-groups', { method: 'POST', body: group })
    byName.set(created.name, created)
  }
  console.log(`  monitor groups: ${byName.size}`)
  return byName
}

async function seedMonitors({ groups, templates, notifications }) {
  const created = []
  const defaultNotifications = DEFAULT_NOTIFICATIONS.map((name) => notifications.get(name))
  for (const monitor of MONITORS) {
    const payload = {
      name: monitor.name,
      type: monitor.type,
      active: monitor.active !== false,
      description: monitor.description ?? '',
      interval_seconds: monitor.interval_seconds,
      retries: monitor.retries ?? 0,
      retries_interval_seconds: monitor.retries_interval_seconds ?? 60,
      timeout_seconds: monitor.timeout_seconds,
      resend_interval_seconds: 0,
      upside_down: monitor.upside_down ?? false,
      run_on: 'all',
      tags: monitor.tags ?? '',
      cert_watch: monitor.cert_watch ?? false,
      cert_notify: monitor.cert_notify ?? false,
      cert_warn_days: monitor.cert_warn_days ?? '',
      config: monitor.config,
      notification_ids: defaultNotifications,
      group_id: groups.get(monitor.group).id,
    }
    if (monitor.template) {
      payload.template_uuid = templates.get(monitor.template).uuid
    }
    const row = await api('/api/monitors', { method: 'POST', body: payload })
    if (monitor.active === false) {
      // `active` is a `default:true` column, so GORM inserts the default when the
      // create body carries false: a monitor can only be paused after it exists.
      // This is also what stops the scheduler from probing the row, which is what
      // the "paused" history of the fixtures means.
      await api(`/api/monitors/${row.id}/pause`, { method: 'POST' })
    }
    created.push({ ...monitor, id: row.id })
    console.log(`  monitor #${row.id} ${row.name} (${row.type})${monitor.active === false ? ' [paused]' : ''}`)
  }
  return created
}

async function seedStatusPages(monitors, groups) {
  const idByName = new Map(monitors.map((monitor) => [monitor.name, monitor.id]))
  for (const page of STATUS_PAGES) {
    const payload = {
      slug: page.slug,
      title: page.title,
      description: page.description,
      footer_text: page.footer_text,
      theme: page.theme,
      is_public: page.is_public,
      show_uptime: page.show_uptime,
      show_charts: page.show_charts,
      show_tags: page.show_tags,
      show_cert_expiry: page.show_cert_expiry,
      uptime_window_hours: page.uptime_window_hours,
      monitor_ids: page.monitors.map((name) => idByName.get(name)),
    }
    const created = await api('/api/status-pages', { method: 'POST', body: payload })
    const links = page.groups.map((link, index) => ({
      group_id: groups.get(link.name).id,
      display_name: link.display_name,
      sort_order: index + 1,
    }))
    await api(`/api/status-pages/${created.id}/groups`, { method: 'PUT', body: { groups: links } })
    console.log(`  status page /status/${created.slug} (${links.length} groups, ${payload.monitor_ids.length} monitors)`)
  }
}

async function seedTokens() {
  for (const token of TOKENS) {
    const created = await api('/api/tokens', { method: 'POST', body: { name: token.name, scopes: token.scopes, expires_in_days: token.expires_in_days } })
    if (token.revoke) {
      await api(`/api/tokens/${created.id}/revoke`, { method: 'POST' })
    }
    console.log(`  token ${created.name}${token.revoke ? ' (revoked)' : ''}`)
  }
}

async function seedIPRules() {
  for (const rule of IP_RULES) {
    await api('/api/ip-rules', { method: 'POST', body: rule })
  }
  console.log(`  ip rules: ${IP_RULES.length}`)
}
/**
 * seedSystems creates the machines of the Systems page.
 *
 * The POST is the real one: the server mints the token, derives the KEY hint and
 * stores the row. Nothing is started to answer it, which is why the samples below
 * are written by hand - a system whose `last_sample_at` is fresh reads `up`, and
 * `connected` only means "an agent socket is open right now", which the cards do
 * not show.
 */
async function seedSystems() {
  const created = []
  for (const system of SYSTEMS) {
    const row = await api('/api/systems', { method: 'POST', body: { name: system.name, host: system.host } })
    created.push({ ...system, id: row.id })
    console.log(`  system #${row.id} ${row.name} (${system.containers.length} containers, ${Object.keys(system.smart).length} disks)`)
  }
  return created
}

/** containerRunning mirrors the badge rule of the containers table. */
function containerRunning(status) {
  return /^up/i.test(status ?? '')
}

/** clamp keeps a generated counter inside the range the charts can draw. */
function clamp(value, min, max) {
  return Math.min(max, Math.max(min, value))
}

/** round trims a float to the digits the columns keep. */
function round(value, digits) {
  const factor = 10 ** digits
  return Math.round(value * factor) / factor
}

/** quote escapes a value for a single quoted SQL literal. */
function quote(value) {
  return String(value).replace(/\\/g, '\\\\').replace(/'/g, "''")
}

/**
 * systemSamples builds the 24 h of one machine.
 *
 * The shape is a slow wave plus a little noise, so the charts are curves instead
 * of flat lines, and a reader notices the traffic peak of the edge box. `rnd`
 * keeps the whole set reproducible.
 */
function systemSamples(system) {
  const { shape, identity } = system
  const stepMs = 60 * 1000
  const count = 24 * 60
  const lastAt = Math.floor((Date.now() - 60 * 1000) / stepMs) * stepMs
  const bootedAt = lastAt - 11 * 24 * 60 * 60 * 1000
  const samples = []
  for (let index = 0; index < count; index++) {
    const at = lastAt - (count - 1 - index) * stepMs
    const phase = (index / count) * Math.PI * 2
    const cpu = clamp(shape.cpu + Math.sin(phase * 3) * shape.cpuSwing + (rnd() - 0.5) * shape.cpuSwing, 1, 98)
    const memPct = clamp(shape.mem + Math.sin(phase * 1.5) * shape.memSwing + (rnd() - 0.5) * 1.4, 3, 96)
    const diskPct = clamp(shape.disk + (index / count) * shape.diskGrowth + (rnd() - 0.5) * 0.05, 1, 99)
    const memTotal = identity.memory_total
    samples.push({
      at,
      cpu,
      memPct,
      memUsed: Math.round((memPct / 100) * memTotal),
      memTotal,
      swapPct: cpu > 60 ? round(0.6 + rnd() * 1.2, 2) : round(rnd() * 0.4, 2),
      diskPct,
      diskUsed: Math.round((diskPct / 100) * 2000398934016),
      diskTotal: 2000398934016,
      netSent: Math.round(shape.net * (0.4 + rnd())) + (cpu > 55 ? between(400000, 2600000) : 0),
      netRecv: Math.round(shape.netSwing * (0.6 + rnd())) + (cpu > 55 ? between(400000, 3200000) : 0),
      diskRead: between(0, 180000),
      diskWrite: between(0, 420000),
      load: round((cpu / 100) * identity.threads + rnd() * 0.8, 2),
      // The sensor sits a few degrees above idle and follows the load.
      temperature: round(shape.temp + (cpu / 100) * 18 + rnd() * 2, 1),
      uptime: Math.round((at - bootedAt) / 1000),
      containers: system.containers.filter((container) => containerRunning(container.status)).length,
    })
  }
  return samples
}


/** runChecks probes every active monitor for real, so the statuses and the
 *  "last check" column of the table come from genuine probes. */
async function runChecks(monitors) {
  for (const monitor of monitors.filter((row) => row.history !== 'paused')) {
    await api(`/api/monitors/${monitor.id}/check`, { method: 'POST' })
    process.stdout.write('.')
  }
  process.stdout.write('\n')
}

/**
 * fixup repairs the two rows that only tell the truth once a first real check
 * has run: the CDN probe pointed at the favicon, which the origin answers with a
 * 404, and the domain watch needs a target that answers before the expiry job
 * has anything to look up.
 */
async function fixup() {
  const monitors = await api('/api/monitors')
  const byName = new Map(monitors.map((monitor) => [monitor.name, monitor]))

  const cdn = byName.get('CDN assets')
  await api(`/api/monitors/${cdn.id}`, {
    method: 'PUT',
    body: {
      ...cdn,
      description: 'Origin pull of the static assets',
      config: { ...cdn.config, url: 'https://example.com/', method: 'GET' },
    },
  })
  await api(`/api/monitors/${cdn.id}/check`, { method: 'POST' })
  console.log(`  #${cdn.id} ${cdn.name} repointed at the origin root`)

  // The registrable domains behind the https targets: the expiry job below is
  // what fills the certificate and the domain columns of the table.
  for (const name of ['Website', 'Documentation', 'Customer portal', 'Edge TLS certificate']) {
    const monitor = byName.get(name)
    await api(`/api/monitors/${monitor.id}`, {
      method: 'PUT',
      body: { ...monitor, domain_watch: true, domain_notify: true, domain_warn_days: '60,30,14' },
    })
    console.log(`  #${monitor.id} ${monitor.name} now watches its domain`)
  }
}

/**
 * buildSystemBackfill returns the SQL that fills the four hardware tables.
 *
 * The rows are written the way the ingest writes them: one sample per minute, one
 * container row every five minutes, the snapshot of the containers as it stands
 * now, and the SMART report the agent keeps cached. Everything is deleted first
 * because a previous run would otherwise leave its samples behind.
 */
function buildSystemBackfill(systems) {
  const statements = [
    'DELETE FROM system_metrics;',
    'DELETE FROM system_container_metrics;',
    'DELETE FROM system_containers;',
    'DELETE FROM system_smart;',
  ]

  const metricValues = []
  const containerValues = []
  const snapshotValues = []
  for (const system of systems) {
    for (const sample of systemSamples(system)) {
      metricValues.push(
        `(${system.id},${round(sample.cpu, 2)},${round(sample.memPct, 2)},${sample.memUsed},${sample.memTotal},` +
          `${sample.swapPct},${round(sample.diskPct, 2)},${sample.diskUsed},${sample.diskTotal},` +
          `${sample.netSent},${sample.netRecv},${sample.diskRead},${sample.diskWrite},` +
          `${sample.load},${sample.temperature},${sample.uptime},${sample.containers},` +
          `'{"temperatures":{"coretemp":{"Package id 0":${sample.temperature}}}}','${utcSQLTime(sample.at)}')`,
      )
    }
    const newest = utcSQLTime(Date.now() - 60 * 1000)
    for (const container of system.containers) {
      const running = containerRunning(container.status)
      for (let index = 0; index < 24 * 60; index += 5) {
        const at = utcSQLTime(Date.now() - 60 * 1000 - (24 * 60 - index) * 60 * 1000)
        const cpu = running ? round(container.cpu * (0.55 + rnd() * 0.9), 2) : 0
        const mem = running ? round(container.mem_mb * (0.92 + rnd() * 0.14), 1) : 0
        containerValues.push(
          `(${system.id},'${quote(container.name)}',${cpu},${mem},` +
            `${running ? Math.round(container.net_sent * (0.5 + rnd())) : 0},` +
            `${running ? Math.round(container.net_recv * (0.5 + rnd())) : 0},'${at}')`,
        )
      }
      snapshotValues.push(
        `(${system.id},'${quote(container.name)}','${quote(container.image)}','${quote(container.status)}',` +
          `'${quote(container.health ?? '')}','${quote(container.ports ?? '')}',${container.cpu},${container.mem_mb},` +
          `${container.net_sent},${container.net_recv},${container.update_available ? 1 : 0},` +
          `'${newest}',UTC_TIMESTAMP(),UTC_TIMESTAMP())`,
      )
    }
  }

  const METRIC_COLUMNS =
    'INSERT INTO system_metrics (system_id,cpu,mem_pct,mem_used,mem_total,swap_pct,disk_pct,disk_used,disk_total,' +
    'net_sent,net_recv,disk_read,disk_write,load_avg,temperature,uptime,containers,extra,created_at) VALUES '
  for (let index = 0; index < metricValues.length; index += 200) {
    statements.push(METRIC_COLUMNS + metricValues.slice(index, index + 200).join(',') + ';')
  }
  const CONTAINER_COLUMNS = 'INSERT INTO system_container_metrics (system_id,name,cpu,mem_mb,net_sent,net_recv,created_at) VALUES '
  for (let index = 0; index < containerValues.length; index += 200) {
    statements.push(CONTAINER_COLUMNS + containerValues.slice(index, index + 200).join(',') + ';')
  }
  if (snapshotValues.length > 0) {
    statements.push(
      'INSERT INTO system_containers (system_id,name,image,status,health,ports,cpu,mem_mb,net_sent,net_recv,' +
        'update_available,last_seen_at,created_at,updated_at) VALUES ' +
        snapshotValues.join(',') +
        ';',
    )
  }

  // The identity the agent reports on its first handshake, and the freshness the
  // listing derives the badge from (services/system_lease.go).
  for (const system of systems) {
    const { identity } = system
    statements.push(
      `UPDATE systems SET hostname='${quote(identity.hostname)}', os_name='${quote(identity.os_name)}', ` +
        `kernel='${quote(identity.kernel)}', arch='${quote(identity.arch)}', cpu_model='${quote(identity.cpu_model)}', ` +
        `cores=${identity.cores}, threads=${identity.threads}, memory_total=${identity.memory_total}, ` +
        `agent_version='${quote(identity.agent_version)}', smart_interval=${identity.smart_interval}, ` +
        `fingerprint=SHA2(CONCAT('fixture-', id), 256), last_error='', ` +
        `last_seen_at=UTC_TIMESTAMP() - INTERVAL 60 SECOND, ` +
        `last_sample_at=UTC_TIMESTAMP() - INTERVAL 60 SECOND WHERE id=${system.id};`,
    )
    if (Object.keys(system.smart).length > 0) {
      statements.push(
        `INSERT INTO system_smart (system_id,disks,complete,collected_at,updated_at) VALUES (` +
          `${system.id},'${quote(JSON.stringify(system.smart))}',1,` +
          `UTC_TIMESTAMP() - INTERVAL 4 MINUTE,UTC_TIMESTAMP());`,
      )
    }
  }

  // An agent that is not part of the fixture must not keep a lease: the listing
  // prints the owner node of every system it finds one for.
  statements.push('DELETE FROM system_leases;')
  return statements
}

/**
 * report prints what the table will show, which is how the fixture is verified.
 *
 * Two rows are not green on purpose (`NON_GREEN`): the Redis probe must read
 * `down` (its port refuses) and the paused portal must read `maintenance`. The
 * POST of a check returns before the probe's heartbeat is stored - the API
 * retries a failing probe first - so the loop waits for both to hold instead of
 * printing the race.
 */
async function report(monitors) {
  const expected = new Map(
    monitors
      .filter((monitor) => NON_GREEN[monitor.name])
      .map((monitor) => [monitor.name, NON_GREEN[monitor.name]]),
  )
  const settled = (rows) =>
    !rows.some((row) => row.status === 'pending') &&
    rows.every((row) => !expected.has(row.name) || row.status === expected.get(row.name))

  const deadline = Date.now() + 30000
  let rows = await api('/api/monitors')
  while (!settled(rows) && Date.now() < deadline) {
    await sleep(1000)
    rows = await api('/api/monitors')
  }
  const off = rows.filter((row) => expected.has(row.name) && row.status !== expected.get(row.name))
  for (const row of off) {
    console.log(`  ! ${row.name} reads ${row.status}, the fixture expects ${expected.get(row.name)}`)
  }

  const byStatus = {}
  let bars = 0
  for (const monitor of rows) {
    byStatus[monitor.status] = (byStatus[monitor.status] ?? 0) + 1
    if ((monitor.heartbeat_bars ?? []).some((slot) => slot !== '')) {
      bars += 1
    }
  }
  console.log(`  monitors: ${rows.length} | statuses: ${JSON.stringify(byStatus)} | with heartbeat bars: ${bars}`)
  const certificates = rows.filter((monitor) => monitor.certificate).length
  const domains = rows.filter((monitor) => monitor.domain).length
  console.log(`  certificates read: ${certificates} | domains read: ${domains}`)

  // The hardware half is verified the same way: the listing must read `up` (the
  // badge comes from `last_sample_at`), and the two tabs must be populated.
  const systems = await api('/api/systems')
  const containers = await api('/api/systems/containers')
  const smart = await api('/api/systems/smart')
  const up = systems.filter((system) => system.status === 'up').length
  console.log(
    `  systems: ${systems.length} (up ${up}) | containers: ${containers.length} | ` +
      `smart reports: ${smart.length} (${smart.reduce((sum, report) => sum + Object.keys(report.disks ?? {}).length, 0)} disks)`,
  )
  for (const system of systems.filter((row) => row.status !== 'up')) {
    console.log(`  ! ${system.name} reads ${system.status}: last_sample_at=${system.last_sample_at ?? 'null'}`)
  }
}

async function main() {
  const force = process.argv.includes('--force')
  console.log(`seeding ${BASE} (database ${DB_NAME} in ${DB_CONTAINER})`)
  await login()
  console.log('  authenticated')

  // The fixture owns the database: it creates rows instead of upserting them and
  // the backfill below rebuilds the whole heartbeat history with a global
  // DELETE, so a database that already has monitors in it is refused unless the
  // operator says the data is disposable.
  const existing = await api('/api/monitors')
  if (existing.length > 0 && !force) {
    throw new Error(
      `${DB_NAME} already holds ${existing.length} monitor(s): this fixture needs an empty database ` +
        '(the rows are created, never upserted, and the heartbeat history is rebuilt from scratch). ' +
        'Pass --force if the data of this instance is disposable.',
    )
  }

  const notifications = await seedNotifications()
  const templates = await seedTemplates()
  const groups = await seedGroups()
  const monitors = await seedMonitors({ groups, templates, notifications })
  await seedStatusPages(monitors, groups)
  await seedTokens()
  await seedIPRules()
  const systems = await seedSystems()

  console.log(`  backfilling ${BEAT_COUNT} beats x ${monitors.length} monitors (5 min apart, last 24 h)`)
  sql(buildBackfill(monitors))

  console.log('  running one real check per active monitor')
  await runChecks(monitors)

  console.log('  fixing up the CDN probe and the domain watch')
  await fixup()

  console.log('  running the expiry job (certificates and domains)')
  await api('/api/admin/expiry/run', { method: 'POST' })

  // The hardware history is written last on purpose: a system reads `down` three
  // missed polls (3 min) after its last sample, so the badge of the screenshots
  // is only `up` while the fixture is fresh. Everything slow (the probes, the
  // expiry job) therefore runs before this, not after.
  console.log(`  backfilling 1440 samples x ${systems.length} systems, ${systems.reduce((sum, s) => sum + s.containers.length, 0)} containers (1 min apart)`)
  sql(buildSystemBackfill(systems))

  await report(monitors)
  console.log('done - take the screenshots now: a system without an agent reads down after 3 minutes')
}

main().catch((error) => {
  console.error(`seeding failed: ${error.message}`)
  process.exit(1)
})
