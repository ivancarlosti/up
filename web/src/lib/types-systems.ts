/**
 * TypeScript mirror of the hardware monitoring models (internal/models/system*.go).
 *
 * A system is one machine reported by a Beszel agent: Up is the hub, so the agent
 * dials us and we ask it for the counters once a minute.
 */

/** State of a system in the listing (models.SystemStatus). */
export type SystemStatus = 'up' | 'down' | 'pending'

export interface System {
  id: number
  uuid: string
  origin_node_id?: string
  revision?: number
  name: string
  host: string
  enabled: boolean
  /** The first characters of the agent token: the rest exists only in the agent. */
  token_prefix?: string
  fingerprint?: string
  agent_version?: string
  hostname?: string
  os_name?: string
  kernel?: string
  arch?: string
  cpu_model?: string
  cores?: number
  threads?: number
  memory_total?: number
  smart_interval?: number
  last_sample_at?: string | null
  last_seen_at?: string | null
  last_error?: string
  created_at: string
  updated_at: string
  /** Runtime decoration: the badge, the live connection and the owner node. */
  status: SystemStatus
  connected: boolean
  owner_node?: string
  owner_node_name?: string
  /** Only present in the answer that created the system or rotated its token. */
  token?: string
  install_command?: string
  agent_key?: string
}

/** One point of a hardware chart (models.SystemPoint). */
export interface SystemPoint {
  created_at: string
  cpu: number
  mem_pct: number
  mem_used: number
  mem_total: number
  swap_pct: number
  disk_pct: number
  disk_used: number
  disk_total: number
  net_sent: number
  net_recv: number
  disk_read: number
  disk_write: number
  load_avg: number
  temperature: number
  uptime: number
  containers: number
}

/**
 * The most recent sample of one machine (models.SystemMetric).
 *
 * It is not a SystemPoint: it is the stored row, which carries the system id and
 * the raw (not averaged) counters of the last poll.
 */
export interface SystemPointOverview extends SystemPoint {
  system_id: number
}

/** One point of a container chart (models.ContainerPoint). */
export interface ContainerPoint {
  created_at: string
  cpu: number
  mem_mb: number
  net_sent: number
  net_recv: number
}

/** The current state of one container (models.SystemContainer). */
export interface SystemContainer {
  system_id: number
  name: string
  container_id?: string
  image?: string
  status?: string
  health?: string
  ports?: string
  cpu: number
  mem_mb: number
  net_sent: number
  net_recv: number
  update_available: boolean
  last_seen_at: string
  created_at: string
  updated_at: string
  /** Filled by the "all containers" page. */
  system_name?: string
  system_uuid?: string
}

/** One disk of a SMART report (beszel.Smart). */
export interface SmartDisk {
  model_name?: string
  serial_number?: string
  firmware_version?: string
  capacity?: number
  status?: string
  disk_name?: string
  disk_type?: string
  temperature?: number
  attributes?: SmartAttribute[]
}

export interface SmartAttribute {
  id?: number
  name: string
  value?: number
  worst?: number
  threshold?: number
  raw_value: number
  raw_string?: string
  when_failed?: string
}

/** A SMART report of one machine (services.SmartReport). */
export interface SmartReport {
  system_id: number
  system_name: string
  disks: Record<string, SmartDisk> | null
  complete: boolean
  collected_at: string
}

/** Everything the detail page needs, in one answer (models.SystemHistory). */
export interface SystemHistory {
  system: System
  metrics: SystemPoint[]
  containers: SystemContainer[]
  smart?: Record<string, SmartDisk> | null
  smart_at?: string
  smart_complete: boolean
  remote?: boolean
}

/** The KEY value an operator pastes into the agent (services.HostKeyInfo). */
export interface AgentKeyInfo {
  key: string
  fingerprint: string
  minimum_version: string
}

export interface SystemPayload {
  name: string
  host: string
  enabled: boolean
}

/** The windows the hardware charts offer, in hours (models.SystemChartWindows). */
export const SYSTEM_CHART_WINDOWS = [1, 6, 24, 168, 720] as const
