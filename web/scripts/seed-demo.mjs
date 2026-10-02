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

  console.log(`  backfilling ${BEAT_COUNT} beats x ${monitors.length} monitors (5 min apart, last 24 h)`)
  sql(buildBackfill(monitors))

  console.log('  running one real check per active monitor')
  await runChecks(monitors)

  console.log('  fixing up the CDN probe and the domain watch')
  await fixup()

  console.log('  running the expiry job (certificates and domains)')
  await api('/api/admin/expiry/run', { method: 'POST' })

  await report(monitors)
  console.log('done')
}

main().catch((error) => {
  console.error(`seeding failed: ${error.message}`)
  process.exit(1)
})
