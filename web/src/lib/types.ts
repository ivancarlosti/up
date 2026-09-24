import type { ClusterStatus } from './types-platform'

/**
 * TypeScript mirror of the Go models (see internal/models). The API payloads are
 * produced by the same json tags, so these interfaces are the contract between
 * the backend and the UI.
 */

export type MonitorType = 'http' | 'keyword' | 'tcp' | 'dns' | 'ssl'
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
  // cache_buster appends a random uptime_kuma_cachebuster query parameter to
  // every request, so caches and CDNs are skipped.
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
  expires_at: string
  source?: DomainSource
  status: DomainStatus
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
  heartbeats?: HeartbeatSummary[]
  votes?: NodeVote[]
  notification_ids: number[]
  group_ids: number[]
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
}

export type MonitorPayload = Partial<Omit<Monitor, 'id'>> & {
  notification_ids?: number[]
  group_ids?: number[]
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
  copy_groups?: boolean
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
  tags: string
  description: string
  active?: boolean
  notification_ids: number[]
  group_ids: number[]
  /** Certificate watching applied to the monitors created from the template. */
  cert_watch: boolean
  cert_notify: boolean
  cert_warn_days: string
  /** Domain expiration watching applied to the monitors created from the template. */
  domain_watch: boolean
  domain_notify: boolean
  domain_warn_days: string
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
  created_at: string
  updated_at: string
}

export type MonitorTemplatePayload = Partial<MonitorTemplate>

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
  group_ids?: number[]
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
    heartbeats_1h: number
    ws_clients: number
    cluster?: ClusterStatus
  }
}

export * from './types-platform'
