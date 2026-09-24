import { del, get, post, put } from './http'
import type {
  AdminSettings,
  APIToken,
  ClusterNode,
  ClusterSettings,
  ClusterStatus,
  ExpirySettings,
  ExpirySettingsResponse,
  IPRule,
  PeerStatusReport,
  StatusPage,
  StatusPageMonitorItem,
  StatusPageGroupLink,
  WhoisParser,
  WhoisParserPayload,
  WhoisTestResult,
} from './types'

/** Admin API client: status pages, cluster, runtime settings and security. */
export const adminApi = {
  statusPages: () => get<StatusPage[]>('/api/status-pages'),
  statusPage: (id: number) => get<{ page: StatusPage; monitors: StatusPageMonitorItem[] }>(`/api/status-pages/${id}`),
  createStatusPage: (payload: Partial<StatusPage> & { monitor_ids?: number[] }) =>
    post<StatusPage>('/api/status-pages', payload),
  updateStatusPage: (id: number, payload: Partial<StatusPage>) => put<StatusPage>(`/api/status-pages/${id}`, payload),
  deleteStatusPage: (id: number) => del<void>(`/api/status-pages/${id}`),
  statusPageMonitors: (id: number) => get<StatusPageMonitorItem[]>(`/api/status-pages/${id}/monitors`),
  setStatusPageMonitors: (id: number, monitorIds: number[]) =>
    put<StatusPageMonitorItem[]>(`/api/status-pages/${id}/monitors`, { monitor_ids: monitorIds }),
  statusPageGroups: (id: number) =>
    get<StatusPageGroupLink[]>(`/api/status-pages/${id}/groups`).then((data) => data ?? []),
  setStatusPageGroups: (id: number, groupIds: number[]) =>
    put<StatusPageGroupLink[]>(`/api/status-pages/${id}/groups`, {
      groups: groupIds.map((group_id) => ({ group_id })),
    }).then((data) => data ?? []),
  publicStatusPage: (slug: string) => get<StatusPage>(`/api/public/status/${slug}`),

  clusterStatus: () => get<ClusterStatus>('/api/cluster/status'),
  /** peerStatus is the local synchronisation view: cursor, checksums, conflicts. */
  peerStatus: () => get<PeerStatusReport>('/api/cluster/sync/status'),
  clusterNodes: () => get<ClusterNode[]>('/api/cluster/nodes'),
  clusterSettings: () => get<ClusterSettings>('/api/cluster/settings'),
  updateClusterSettings: (payload: Partial<ClusterSettings>) => put<ClusterSettings>('/api/cluster/settings', payload),
  clusterPrivateKey: () => get<{ private_key: string; node_id: string; node_name: string }>('/api/cluster/private-key'),
  regenerateClusterKey: () => post<{ private_key: string; message: string }>('/api/cluster/private-key/regenerate'),
  joinCluster: (payload: { primary_url: string; private_key: string; node_id?: string; node_name?: string }) =>
    post<{ joined: boolean; message: string; status: ClusterStatus }>('/api/cluster/join', payload),
  leaveCluster: (nodeId?: string) => post<ClusterStatus>('/api/cluster/leave', { node_id: nodeId ?? '' }),
  clusterHeartbeat: () => post<{ online: ClusterNode[]; offline: ClusterNode[] }>('/api/cluster/heartbeat'),

  adminSettings: () => get<AdminSettings>('/api/admin/settings'),
  updateAdminSettings: (payload: { default_locale?: string; default_theme?: string; app_name?: string }) =>
    put<void>('/api/admin/settings', payload),

  /** Daily certificate/domain expiration job and the per-TLD WHOIS rules. */
  expirySettings: () => get<ExpirySettingsResponse>('/api/admin/expiry'),
  updateExpirySettings: (payload: ExpirySettings) => put<ExpirySettings>('/api/admin/expiry', payload),
  runExpiryNow: () => post<{ ran: boolean; at: string }>('/api/admin/expiry/run'),
  createWhoisParser: (payload: WhoisParserPayload) => post<WhoisParser>('/api/admin/expiry/whois-parsers', payload),
  updateWhoisParser: (id: number, payload: WhoisParserPayload) =>
    put<WhoisParser>(`/api/admin/expiry/whois-parsers/${id}`, payload),
  deleteWhoisParser: (id: number) => del<void>(`/api/admin/expiry/whois-parsers/${id}`),
  testWhoisParser: (payload: WhoisParserPayload & { domain?: string; raw?: string }) =>
    post<WhoisTestResult>('/api/admin/expiry/whois-parsers/test', payload),

  tokens: () => get<APIToken[]>('/api/tokens'),
  createToken: (payload: { name: string; scopes: string[]; expires_in_days?: number }) =>
    post<APIToken>('/api/tokens', payload),
  updateToken: (id: number, payload: { name?: string; scopes?: string[]; expires_at?: string }) =>
    put<APIToken>(`/api/tokens/${id}`, payload),
  revokeToken: (id: number) => post<void>(`/api/tokens/${id}/revoke`),
  deleteToken: (id: number) => del<void>(`/api/tokens/${id}`),
  ipRules: () => get<{ rules: IPRule[]; client_ip: string; bypassed: boolean }>('/api/ip-rules'),
  createIPRule: (payload: Partial<IPRule>) => post<IPRule>('/api/ip-rules', payload),
  updateIPRule: (id: number, payload: Partial<IPRule>) => put<IPRule>(`/api/ip-rules/${id}`, payload),
  deleteIPRule: (id: number) => del<void>(`/api/ip-rules/${id}`),
}
