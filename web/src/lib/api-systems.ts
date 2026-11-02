import { del, get, post, put } from './http'
import type {
  AgentKeyInfo,
  ContainerPoint,
  SmartReport,
  System,
  SystemContainer,
  SystemHistory,
  SystemPayload,
  SystemPoint,
  SystemPointOverview,
} from './types-systems'

/**
 * systemsApi is the hardware monitoring client: the machines reported by the
 * Beszel agents, their containers and their disks.
 *
 * The agent itself never goes through here: it dials
 * `/api/beszel/agent-connect` with its token, on its own protocol.
 */
export const systemsApi = {
  systems: () => get<System[]>('/api/systems'),
  system: (id: number) => get<System>(`/api/systems/${id}`),
  /**
   * history is the detail page payload: the row, the charts, the containers and
   * the SMART report in one answer (four round trips would let the charts
   * disagree with the container list while they load).
   */
  systemHistory: (id: number, hours = 24) =>
    get<SystemHistory>(`/api/systems/${id}/history`, { hours }),
  systemMetrics: (id: number, hours = 24) => get<SystemPoint[]>(`/api/systems/${id}/metrics`, { hours }),
  /** overview is the last sample of every machine, for the cards of the listing. */
  systemsOverview: () => get<SystemPointOverview[]>('/api/systems/overview'),
  createSystem: (payload: SystemPayload) => post<System>('/api/systems', payload),
  updateSystem: (id: number, payload: SystemPayload) => put<System>(`/api/systems/${id}`, payload),
  deleteSystem: (id: number) => del<void>(`/api/systems/${id}`),
  /** rotateToken replaces the agent token and answers with the new one, once. */
  rotateSystemToken: (id: number) => post<System>(`/api/systems/${id}/token`),
  /** agentKey is the KEY value the agent must be started with. */
  agentKey: () => get<AgentKeyInfo>('/api/systems/agent-key'),
  containers: (id: number) => get<SystemContainer[]>(`/api/systems/${id}/containers`),
  containerMetrics: (id: number, name: string, hours = 24) =>
    get<ContainerPoint[]>(`/api/systems/${id}/containers/${encodeURIComponent(name)}/metrics`, { hours }),
  allContainers: () => get<SystemContainer[]>('/api/systems/containers'),
  smart: () => get<SmartReport[]>('/api/systems/smart'),
  systemSmart: (id: number) => get<SmartReport[]>(`/api/systems/${id}/smart`),
  /** refreshSmart asks the node that owns the agent for a fresh report. */
  refreshSmart: (id: number) => post<SmartReport[]>(`/api/systems/${id}/smart/refresh`),
}
