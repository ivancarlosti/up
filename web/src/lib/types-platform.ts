/**
 * Types of the platform features: notifications, cluster, status pages, API
 * tokens, IP rules, authentication and settings.
 */

import type { AggregateStatus, FailureStrategy, Header, Monitor, NotificationSenderStrategy, NodeStatus, NodeUnavailableStrategy, ThemeMode } from './types'

export interface ClusterSettings {
  id: number
  failure_strategy: FailureStrategy
  node_unavailable_strategy: NodeUnavailableStrategy
  notification_sender: NotificationSenderStrategy
  updated_at: string
}

export interface SMTPConfig {
  host: string
  port: number
  username: string
  password: string
  from: string
  to: string
  secure: boolean
  use_html: boolean
  skip_tls_verify: boolean
  subject_prefix: string
}

export interface WebhookConfig {
  url: string
  method: string
  content_type: string
  headers: Header[]
  body_template: string
}

export interface SlackConfig {
  token: string
  channel: string
  bot_name: string
  icon: string
  thread_ts: string
}

export interface DiscordConfig {
  webhook_id: string
  token: string
  username: string
  avatar_url: string
  thread_id: string
}

export interface TelegramConfig {
  token: string
  chats: string
  parse_mode: string
  disable_notification: boolean
  disable_preview: boolean
}

export interface NotificationConfig {
  smtp?: SMTPConfig
  webhook?: WebhookConfig
  slack?: SlackConfig
  discord?: DiscordConfig
  telegram?: TelegramConfig
}

export type NotificationType = 'smtp' | 'webhook' | 'slack' | 'discord' | 'telegram'

export interface Notification {
  id: number
  name: string
  type: NotificationType
  active: boolean
  is_default: boolean
  resend_interval_seconds: number
  config: NotificationConfig
  created_at: string
  updated_at: string
  monitor_ids: number[]
}

export interface ClusterNode {
  id: number
  name: string
  node_id: string
  api_url: string
  last_heartbeat: string | null
  status: NodeStatus
  is_primary: boolean
  created_at: string
  updated_at: string
  is_self: boolean
  version?: string
}

export interface ClusterStatus {
  enabled: boolean
  node_id: string
  node_name: string
  is_primary: boolean
  private_key?: string
  settings: ClusterSettings
  nodes: ClusterNode[]
  online_nodes: number
  total_nodes: number
  offline_node_names: string[] | null
}

/**
 * PeerSyncSummary is the synchronisation health a node can see by itself.
 *
 * The failure modes of a federated cluster are quiet: a reference whose target never
 * arrived, an edit that lost the merge, a change that could not be applied, and a
 * partition that shrinks the vote denominator are all invisible in a peer table.
 */
export interface PeerSyncSummary {
  pending_links: number
  pending_links_given_up: number
  conflicts: number
  dead_letters: number
  dead_letters_skipped: number
  /** With the QUORUM strategy the denominator is the REACHABLE nodes (decision D7). */
  quorum_known: number
  quorum_reachable: number
  quorum_settled: number
}

/** PeerStatusView is one peer as this node currently sees it. */
export interface PeerStatusView {
  peer_node_id: string
  peer_name: string
  api_url: string
  peer_version: string
  protocol_version: number
  node_status: NodeStatus
  peer_status: string
  settled: boolean
  online_since: string | null
  last_seen_at: string | null
  last_success_at: string | null
  last_error: string
  last_error_at: string | null
  /** Cursor into the peer's outbox: one that stops advancing is a stalled sync. */
  last_change_id: number
  last_manifest_at: string | null
  /** False means the checksums disagree: the peer is diverging, not merely behind. */
  last_manifest_ok: boolean
}

export interface ConflictView {
  entity: string
  uuid: string
  kept_origin: string
  kept_revision: number
  lost_origin: string
  lost_revision: number
  detected_at: string
}

export interface DeadLetterView {
  change_id: number
  entity: string
  uuid: string
  attempts: number
  skipped: boolean
  last_error: string
  updated_at: string
}

/** PeerStatusReport is GET /api/cluster/sync/status (the local sync view). */
export interface PeerStatusReport {
  node_id: string
  mode: string
  peer_api_enabled: boolean
  version: string
  protocol_version: number
  settle_seconds: number
  peers: PeerStatusView[]
  sync: PeerSyncSummary
  recent_conflicts: ConflictView[] | null
  recent_dead_letters: DeadLetterView[] | null
}

export interface StatusPage {
  id: number
  slug: string
  title: string
  description: string
  footer_text: string
  theme: ThemeMode
  is_public: boolean
  show_uptime: boolean
  show_charts: boolean
  show_tags: boolean
  /** Publishes the certificate/domain badges of the monitors on the page. */
  show_expiry: boolean
  /**
   * Period the page shows the uptime and the bars for (24, 168, 336 or 720
   * hours). 0 inherits the global window from Admin > Settings.
   */
  uptime_window_hours: number
  custom_css: string
  created_at: string
  updated_at: string
  monitors?: Monitor[]
  groups?: StatusPageGroupSection[]
  monitors_count: number
  groups_count?: number
  overall_status: AggregateStatus
  up_monitors: number
  down_monitors: number
}

/** A rendered section of the public status page (a group, or the ungrouped ones). */
export interface StatusPageGroupSection {
  name: string
  monitors: Monitor[]
}

/** A monitor group included in a status page (admin payload). */
export interface StatusPageGroupLink {
  group_id: number
  display_name?: string
  sort_order?: number
  group_name?: string
}

export interface StatusPageMonitorItem {
  id: number
  status_page_id: number
  monitor_id: number
  display_name: string
  group_name: string
  sort_order: number
  show_uptime: boolean
  show_chart: boolean
}

export type TokenScope = 'read' | 'write'

export interface APIToken {
  id: number
  name: string
  prefix: string
  scopes: TokenScope[]
  expires_at: string | null
  last_used_at: string | null
  last_used_ip: string
  revoked_at: string | null
  created_at: string
  token?: string
}

export type IPRuleAction = 'allow' | 'deny'
export type IPRuleScope = 'all' | 'dashboard' | 'api' | 'public'

export interface IPRule {
  id: number
  cidr: string
  action: IPRuleAction
  scope: IPRuleScope
  note: string
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface Identity {
  email: string
  method: string
  auth_method: string
  expires_at?: string
}

export interface PublicSettings {
  app_name: string
  version: string
  auth_method: 'none' | 'account' | 'keycloak'
  auth_enabled: boolean
  recaptcha_enabled: boolean
  recaptcha_client_id: string
  default_locale: string
  default_theme: ThemeMode
  time_format: string
  supported_locales: string[]
  supported_themes: ThemeMode[]
  supported_time_formats: string[]
  cluster_enabled: boolean
  node_id: string
  node_name: string
  /** Global uptime window in hours (24, 168, 336 or 720). */
  uptime_window_hours: number
  /** Every window the settings page offers. */
  uptime_windows: number[]
}

export interface SessionResponse {
  authenticated: boolean
  auth_method: string
  login_enabled: boolean
  oidc_enabled: boolean
  recaptcha_enabled: boolean
  recaptcha_client_id: string
  instance_url: string
  identity?: Identity
}

export interface AdminSettings {
  default_locale: string
  default_theme: ThemeMode
  time_format: string
  supported_time_formats: string[]
  app_name: string
  app_url: string
  supported_locales: string[]
  supported_themes: ThemeMode[]
  auth_method: string
  cluster_enabled: boolean
  version: string
  settings: Record<string, string>
  /** Days of heartbeat history kept (0 = never purge). */
  heartbeat_retention_days: number
  /** The documented default, shown as the placeholder. */
  default_retention_days: number
  /** Upper bound accepted by the API. */
  max_retention_days: number
  /** Global uptime window in hours (24, 168, 336 or 720). */
  uptime_window_hours: number
  /** Every window the settings page offers. */
  uptime_windows: number[]
}

/** Response of GET /api/admin/maintenance/heartbeats. */
export interface HeartbeatRetentionStats {
  retention_days: number
  default_days: number
  max_days: number
  total: number
  oldest: string | null
  newest: string | null
  /** Heartbeats a purge with the current policy would delete. */
  would_delete: number
  /** Timestamp of the cut-off (absent when retention is 0). */
  cutoff?: string
}

/** Response of POST /api/admin/maintenance/heartbeats/purge. */
export interface HeartbeatPurgeResult {
  deleted: number
  days: number
  before: string
}

/** ExpirySettings is the daily certificate/domain job configuration. */
export interface ExpirySettings {
  check_time: string
  check_timezone: string
  rdap_enabled: boolean
  whois_enabled: boolean
  rate_limit_ms: number
  timeout_seconds: number
}

/** WhoisParser is an operator provided rule for one TLD. */
export interface WhoisParser {
  id: number
  tld: string
  server: string
  expiry_regex: string
  date_layouts: string
  not_found_pattern: string
  min_interval_ms: number
  enabled: boolean
  note: string
  created_at: string
  updated_at: string
}

export type WhoisParserPayload = Partial<Omit<WhoisParser, 'id' | 'created_at' | 'updated_at'>>

/** Response of GET /api/admin/expiry. */
export interface ExpirySettingsResponse {
  settings: ExpirySettings
  parsers: WhoisParser[]
  defaults: ExpirySettings
  last_run_day: number
  next_run: string
}

/** Response of POST /api/admin/expiry/whois-parsers/test. */
export interface WhoisTestResult {
  raw: string
  ok: boolean
  not_found: boolean
  error?: string
  expires_at?: string
  days_left?: number
}

/** The two things the daily expiry job refreshes. */
export type ExpiryTargetKind = 'certificate' | 'domain'

/** One monitor of an expiry target, with the observation stored for it. */
export interface ExpiryTargetMonitor {
  id: number
  name: string
  status?: string
  days_left?: number
  expires_at?: string
  checked_at?: string
}

/**
 * ExpiryTarget is one deduplicated unit of work of the daily job: the endpoint
 * behind a TLS handshake or the registrable domain behind a registry lookup.
 */
export interface ExpiryTarget {
  kind: ExpiryTargetKind
  /** Dedup identity: "host:port|sni" for a certificate, the eTLD+1 for a domain. */
  key: string
  label: string
  address?: string
  server_name?: string
  domain?: string
  /** Manual: the target only exists because of the date typed in the monitor. */
  manual: boolean
  monitors: ExpiryTargetMonitor[]
  status?: string
  days_left?: number
  expires_at?: string
  checked_at?: string
}

/** Response of GET /api/admin/expiry/targets. */
export interface ExpiryTargetsResponse {
  targets: ExpiryTarget[]
  count: number
}

/** Response of POST /api/admin/expiry/targets/refresh. */
export interface ExpiryTargetRefreshResult {
  ran: boolean
  kind: ExpiryTargetKind
  target: string
  monitors: number
  at: string
}
