import type { ClusterStatus } from './types-platform'

/**
 * TypeScript mirror of the Go models (see internal/models). The API payloads are
 * produced by the same json tags, so these interfaces are the contract between
 * the backend and the UI.
 */

export type MonitorType = 'http' | 'keyword' | 'tcp' | 'dns' | 'ssl' | 'websocket'
/**
 * Address family a probe prefers (mirror of `models.IPFamily`).
 *
 * - `auto` (default) lets the dialer choose (happy eyeballs: IPv6 first when the
 *   name has an AAAA, the other family 300 ms later), so a dead path of a dual
 *   stack host hides behind the working one;
 * - `alternate` flips between IPv4 and IPv6 on every execution, so that dead path
 *   is reported down;
 * - `ipv4`/`ipv6` pin one family for good, and the probe fails when the target
 *   has no address in it.
 */
export type IPFamily = 'auto' | 'alternate' | 'ipv4' | 'ipv6'
export type AggregateStatus = 'up' | 'down' | 'degraded' | 'pending' | 'maintenance' | 'unknown'
export type HeartbeatStatus = 'up' | 'down' | 'pending' | 'maintenance'
export type FailureStrategy = 'ANY_NODE_FAILS' | 'ALL_NODES_FAIL' | 'QUORUM'
export type NodeUnavailableStrategy = 'IGNORE' | 'MARK_DEGRADED'
export type NotificationSenderStrategy = 'PRIMARY_ONLY' | 'ANY_WITH_LOCK'
export type NodeStatus = 'online' | 'degraded' | 'offline'
export type ThemeMode = 'system' | 'light' | 'dark'

export interface Header {
  key: string
  value: string
}

export interface MonitorConfig {
  url?: string
  method?: string
  encoding?: string
  body?: string
  headers?: Header[]
  auth_type?: string
  basic_user?: string
  basic_pass?: string
  bearer_token?: string
  ignore_tls?: boolean
  max_redirects?: number
  // cache_buster appends a random up_cachebuster query parameter to every
  // request, so caches and CDNs are skipped.
  cache_buster?: boolean
  accepted_status_codes?: string
  keyword?: string
  invert_keyword?: boolean
  case_sensitive?: boolean
  host?: string
  port?: number
  send?: string
  expect?: string
  hostname?: string
  resolver_server?: string
  record_type?: string
  expected_value?: string
  invert_check?: boolean
  // SNI of a ssl monitor (the certificate usually names a virtual host).
  server_name?: string
  /**
   * Strategy of the Lighthouse web performance run of this monitor (see
   * `lighthouse_enabled`): `mobile` (default), `desktop` or `both` (the two of
   * them, which costs two PageSpeed Insights calls per measurement). Only the
   * two HTTP(s) types carry it.
   */
  lighthouse_strategy?: LighthouseStrategy
  /**
   * How often the page is measured (see `lighthouse_enabled`): `hourly`, `6h`,
   * `12h`, `daily` or `weekly` (default). Absent means the cadence of the job
   * (Admin > Web performance), which is what a monitor stored before the field
   * existed follows.
   */
  lighthouse_interval?: LighthouseInterval
  /**
   * Address family the probe prefers. Shared by every type, like `ignore_tls`:
   * see `IPFamily`. Absent means the backend default (`auto`).
   */
  ip_family?: IPFamily
}

/**
 * The runs PageSpeed Insights really accepts: a mobile or a desktop run (mirror
 * of `models.LighthouseRuns`). One observation and one point of the history
 * always describe one of these.
 */
export type LighthouseRunStrategy = 'mobile' | 'desktop'

/**
 * What a monitor (or the job) can be configured with: one run, or `both` of them
 * (mirror of `models.LighthouseStrategies`). `both` is never a persisted
 * observation: it is two runs, and therefore two rows.
 */
export type LighthouseStrategy = LighthouseRunStrategy | 'both'

/**
 * The cadences a monitor can measure its page with (mirror of
 * `models.LighthouseIntervals`): a discrete set, so the API, the form and the
 * stored configuration cannot disagree about what a cadence is.
 */
export type LighthouseInterval = 'hourly' | '6h' | '12h' | 'daily' | 'weekly'

/** The weekday a weekly measurement is anchored to (mirror of `models.LighthouseWeekdays`). */
export type LighthouseWeekday = 'sunday' | 'monday' | 'tuesday' | 'wednesday' | 'thursday' | 'friday' | 'saturday'

/** Outcome of the last Lighthouse run stored for a monitor. */
export type LighthouseStatus = 'ok' | 'error'

/**
 * LighthouseMeasurements is what one run measured: the four category scores
 * (0..100, null when the API could not measure one) and the Core Web Vitals.
 */
export interface LighthouseMeasurements {
  performance: number | null
  accessibility: number | null
  best_practices: number | null
  seo: number | null
  /** Core Web Vitals: milliseconds, except the unitless layout shift. */
  first_contentful_paint_ms: number
  largest_contentful_paint_ms: number
  total_blocking_time_ms: number
  cumulative_layout_shift: number
  speed_index_ms: number
}

/** LighthouseResult is the last observation stored for a monitor (one run). */
export interface LighthouseResult extends LighthouseMeasurements {
  status: LighthouseStatus
  /** Why the run failed (only when status is "error"). */
  message?: string
  /** The run this observation describes: mobile or desktop (never "both"). */
  strategy: LighthouseRunStrategy
  /** The page Lighthouse actually audited, after the redirects. */
  final_url?: string
  ran_at: string
  fetch_time?: string
  ran_by_node?: string
  /**
   * The cadence this observation was measured with and when the next one is due.
   * Both are computed by the backend for the listing: they are absent from a
   * result that was not decorated (a manual "measure now", for instance).
   */
  interval?: LighthouseInterval
  next_run?: string
}

/** LighthouseRun is one point of the history the performance page charts. */
export interface LighthouseRun extends LighthouseMeasurements {
  id: number
  monitor_id: number
  ran_at: string
  strategy: LighthouseRunStrategy
  status: LighthouseStatus
  message?: string
  ran_by_node?: string
}

/** CertificateInfo is the TLS certificate read by the probe of a monitor. */
export interface CertificateInfo {
  subject: string
  issuer: string
  serial?: string
  not_before: string
  not_after: string
  dns_names?: string[]
  days_left: number
  captured_at: string
  captured_by_node?: string
}

/** Where a domain expiry date came from. */
export type DomainSource = 'manual' | 'rdap' | 'whois'

/** Outcome of the last registry lookup. */
export type DomainStatus = 'ok' | 'not_found' | 'unsupported' | 'error'

/** DomainInfo is the registry expiration observed for a monitor. */
export interface DomainInfo {
  domain: string
  registrar?: string
  /** Registry expiry date, or null when the lookup read none (not_found/unsupported/error). */
  expires_at: string | null
  source?: DomainSource
  status: DomainStatus
  /** Registry status list from the RDAP response (e.g. "client transfer prohibited"). Absent for manual dates, WHOIS lookups and errors. */
  rdap_status?: string[]
  error?: string
  days_left: number
  checked_at: string
  checked_by_node?: string
}

export interface HeartbeatSummary {
  status: HeartbeatStatus
  latency_ms: number
  created_at: string
  node_id?: string
}

export interface NodeVote {
  node_id: string
  node_name: string
  status: AggregateStatus
  latency_ms: number
  message: string
  checked_at: string | null
  online: boolean
}

export interface Monitor {
  id: number
  name: string
  type: MonitorType
  active: boolean
  description: string
  interval_seconds: number
  retries: number
  retries_interval_seconds: number
  timeout_seconds: number
  resend_interval_seconds: number
  upside_down: boolean
  run_on: 'all' | 'primary' | 'node' | 'some'
  /** The subset used by run_on=some: comma separated node ids. */
  run_on_nodes: string
  node_id: string
  tags: string
  config: MonitorConfig
  created_at: string
  updated_at: string

  // Runtime fields filled by the backend on every listing.
  status: AggregateStatus
  last_check_at: string | null
  last_latency_ms: number
  uptime_24h: number
  uptime_7d: number
  uptime_30d: number
  /** Uptime over the configured window (see `uptime_hours`). */
  uptime: number
  /** The window `uptime` covers: 24, 168, 336 or 720 hours. */
  uptime_hours: number
  /**
   * Compact, bucketed heartbeat history for the monitors table: one status per
   * slot, oldest first, empty string when the slot saw no heartbeat. It is a
   * snapshot (never updated live) so the column stays cheap to render.
   */
  heartbeat_bars?: string[]
  heartbeats?: HeartbeatSummary[]
  votes?: NodeVote[]
  notification_ids: number[]
  /**
   * The single group of the monitor: the id a write uses (0 when it belongs to no
   * group) and the name the table and the notification bodies read.
   */
  group_id: number
  group_name: string
  // Certificate watching (see docs/monitors.md §9).
  cert_watch: boolean
  cert_notify: boolean
  /** Free form list of days before expiry, e.g. "7,6,5,30". */
  cert_warn_days: string
  /** Last certificate read by a probe (only when cert_watch is on). */
  certificate?: CertificateInfo
  // Domain expiration watching (the registry counterpart of the certificate).
  domain_watch: boolean
  domain_notify: boolean
  /** Free form list of days before expiry for the domain registration. */
  domain_warn_days: string
  /** Manual expiration date (ISO date) for TLDs that publish none. */
  domain_expires_at: string | null
  /** Last registry expiration read (only when domain_watch is on). */
  domain?: DomainInfo
  // Web performance (Google Lighthouse through PageSpeed Insights).
  /**
   * Score the page of this monitor with the daily job. Only the two HTTP(s)
   * types offer it; the strategy lives in `config.lighthouse_strategy`.
   */
  lighthouse_enabled: boolean
  /**
   * Last score read by the job (only when lighthouse_enabled is on): the run of
   * the strategy the monitor is configured with, or the newest one when it
   * measures both.
   */
  lighthouse?: LighthouseResult
  /**
   * Every stored score of the monitor, keyed by run (`mobile`, `desktop`): a
   * monitor configured with `both` has two, and the UI renders one score block
   * per entry.
   */
  lighthouses?: Record<string, LighthouseResult>
  /**
   * The template this monitor follows (its uuid), empty when it follows none.
   * Editing the template pushes its defaults to every linked monitor.
   */
  template_uuid: string
  /** Name of the linked template, filled by the backend decoration. */
  template_name?: string
}

export type MonitorPayload = Partial<Omit<Monitor, 'id'>> & {
  notification_ids?: number[]
  /**
   * The single group of the monitor. A request that omits it keeps the group the
   * monitor already has, while 0 moves it to no group.
   */
  group_id?: number
}

/**
 * MonitorGroup is a named collection of monitors. Groups organise the monitor
 * list and drive the membership of a status page (a page that includes a group
 * shows every monitor inside it).
 */
export interface MonitorGroup {
  id: number
  uuid: string
  name: string
  description: string
  color: string
  sort_order: number
  created_at: string
  updated_at: string
  monitor_ids: number[]
  monitor_count: number
}

export type MonitorGroupPayload = Partial<MonitorGroup> & { monitor_ids?: number[] }

/** Options of a monitor clone (POST /api/monitors/:id/clone). */
export interface MonitorCloneOptions {
  name?: string
  copy_notifications?: boolean
  /** Puts the copy in the same group as the original. */
  copy_group?: boolean
}

/** Options of a group clone (POST /api/monitor-groups/:id/clone). */
export interface MonitorGroupCloneOptions {
  name?: string
  deep?: boolean
  copy_links?: boolean
}

/** TemplateDefaults is the scheduling part of a monitor template. */
export interface TemplateDefaults {
  interval_seconds: number
  retries: number
  retries_interval_seconds: number
  timeout_seconds: number
  resend_interval_seconds: number
  run_on: 'all' | 'primary' | 'node' | 'some'
  /** The subset used by run_on=some: comma separated node ids. */
  run_on_nodes: string
  node_id: string
  description: string
  active?: boolean
  notification_ids: number[]
  /** Certificate watching applied to the monitors created from the template. */
  cert_watch: boolean
  cert_notify: boolean
  cert_warn_days: string
  /** Domain expiration watching applied to the monitors created from the template. */
  domain_watch: boolean
  domain_notify: boolean
  domain_warn_days: string
  /**
   * Web performance applied to the monitors created from the template (the
   * strategy is part of `config.lighthouse_strategy`).
   */
  lighthouse_enabled: boolean
}

/**
 * MonitorTemplate is a reusable monitor blueprint: the probe type and its
 * configuration plus the defaults applied to every monitor created or edited
 * from it. The target of the probe is never part of it.
 */
export interface MonitorTemplate {
  id: number
  uuid: string
  name: string
  description: string
  type: MonitorType
  config: MonitorConfig
  defaults: TemplateDefaults
  /**
   * The scope of the last link run (see TemplateLinkScope). It is stored on the
   * template, so the link dialog reopens on the selection the operator last made
   * instead of falling back to "every monitor of this type".
   */
  link_scope: TemplateLinkScope
  /** Push the defaults to the linked monitors whenever the template is edited. */
  propagate: boolean
  /**
   * How many monitors follow this template (the link is the `template_uuid` of a
   * monitor). The list endpoint fills it with one grouped count, so the admin
   * table can show and sort by it.
   */
  monitor_count: number
  created_at: string
  updated_at: string
}

export type MonitorTemplatePayload = Partial<MonitorTemplate>

/** The three ways a link run can select "the monitors of a template". */
export type TemplateLinkScopeKind = 'type' | 'groups' | 'tags'

/**
 * TemplateLinkScope is the selection of the last link run of a template.
 *
 * `groups` are stored as UUIDs and never as local ids: the template is
 * synchronised between the nodes of a cluster, where the ids of the groups differ
 * and the UUIDs do not. `tags` are compared as whole tags, case insensitively
 * ("prod" never matches "production").
 *
 * Both `groups` and `tags` have an outside, so a run with one of them also
 * detaches the followers of the template that fall outside the selection.
 */
export interface TemplateLinkScope {
  kind: TemplateLinkScopeKind
  group_uuids?: string[]
  tags?: string[]
}

/** What a "link the monitors of a template" run reports. */
export interface TemplateLinkResult {
  monitors: number
  linked: number
  updated: number
  /**
   * The monitors that follow the template from outside the scope of the run.
   * A run with an outside (groups, tags) makes the selection the authoritative
   * scope of the template, so those monitors stop following it (they keep their
   * row, their address, their tags and their groups); the "every monitor of this
   * type" scope has no outside and detaches nobody, which is why this is 0 there.
   */
  unlinked: number
  dry_run: boolean
  /**
   * The scope the run applied, with the groups and the tags normalized and pruned
   * of what no longer exists. A dry run reports the scope it would store.
   */
  scope: TemplateLinkScope
}

/** TagUsage is one tag in use and how many monitors carry it. */
export interface TagUsage {
  tag: string
  monitors: number
}

/**
 * Options of a bulk tag edit. The three selectors are a union: the ids the
 * operator ticked, the monitors of the given groups, or the monitors carrying the
 * given tag (which is what makes a rename possible).
 */
export interface BulkTagOptions {
  monitor_ids?: number[]
  group_ids?: number[]
  tag?: string
  add?: string[]
  remove?: string[]
  dry_run?: boolean
}

/** BulkTagChange is what a bulk tag run did (or would do) on one monitor. */
export interface BulkTagChange {
  monitor_id: number
  name: string
  tags_before: string
  tags_after: string
  status: 'dry_run' | 'updated' | 'failed'
  error?: string
}

/** BulkTagReport is the answer of a bulk tag run. */
export interface BulkTagReport {
  selected: number
  updated: number
  unchanged: number
  failed: number
  dry_run: boolean
  changes: BulkTagChange[]
}

/** FieldChange is one difference between a monitor and a template. */
export interface FieldChange {
  field: string
  from: string
  to: string
}

/** ApplyResult reports what happened (or would happen) to one monitor. */
export interface ApplyResult {
  monitor_id: number
  name: string
  changed?: FieldChange[]
  applied: boolean
  error?: string
}

export interface ApplyTemplateOptions {
  monitor_ids: number[]
  fields?: string[]
  dry_run?: boolean
}

/** BulkRowResult is the outcome of one pasted line. */
export interface BulkRowResult {
  line: number
  name: string
  status: 'dry_run' | 'created' | 'duplicate' | 'invalid' | 'failed'
  monitor_id?: number
  error?: string
}

export interface BulkReport {
  parsed: number
  created: number
  dry_run: number
  skipped: number
  failed: number
  rows: BulkRowResult[]
}

export interface BulkOptions {
  text: string
  template_id: number
  /**
   * The single group the new monitors join (0 when they join none). A request that
   * omits it falls back to the group the template carries.
   */
  group_id?: number
  active?: boolean
  dry_run?: boolean
}

export interface Heartbeat {
  id: number
  monitor_id: number
  node_id: string
  status: HeartbeatStatus
  latency_ms: number
  status_code: number
  message: string
  important: boolean
  created_at: string
}

export interface UptimeStats {
  monitor_id: number
  hours: number
  up: number
  down: number
  pending: number
  total: number
  uptime: number
  avg_ms: number
  min_ms: number
  max_ms: number
  p95_ms: number
}

export interface NotificationLog {
  id: number
  notification_id: number
  monitor_id: number
  event: 'down' | 'up' | 'test' | 'cert_expiring' | 'cert_expired' | 'domain_expiring' | 'domain_expired'
  success: boolean
  error: string
  duration_ms: number
  node_id: string
  created_at: string
}

export interface DashboardResponse {
  monitors: Monitor[]
  summary: {
    total: number
    up: number
    down: number
    degraded: number
    pending: number
    maintenance: number
    unknown: number
    paused: number
    ws_clients: number
    cluster?: ClusterStatus
  }
}

export * from './types-platform'
