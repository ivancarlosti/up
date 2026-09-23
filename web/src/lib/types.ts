import type { ClusterStatus } from './types-platform'

/**
 * TypeScript mirror of the Go models (see internal/models). The API payloads are
 * produced by the same json tags, so these interfaces are the contract between
 * the backend and the UI.
 */

export type MonitorType = 'http' | 'keyword' | 'tcp' | 'dns'
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
  run_on: 'all' | 'primary' | 'node'
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
  event: 'down' | 'up' | 'test'
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
