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

export interface NotificationConfig {
  smtp?: SMTPConfig
  webhook?: WebhookConfig
}

export type NotificationType = 'smtp' | 'webhook'

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
  supported_locales: string[]
  supported_themes: ThemeMode[]
  cluster_enabled: boolean
  node_id: string
  node_name: string
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
  app_name: string
  app_url: string
  supported_locales: string[]
  supported_themes: ThemeMode[]
  auth_method: string
  cluster_enabled: boolean
  version: string
  settings: Record<string, string>
}
