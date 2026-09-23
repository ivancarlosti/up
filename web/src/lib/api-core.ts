import { buildURL, del, get, post, put } from './http'
import type { Query } from './http'
import type {
  DashboardResponse,
  Heartbeat,
  Identity,
  Monitor,
  MonitorCloneOptions,
  MonitorGroup,
  MonitorGroupCloneOptions,
  MonitorGroupPayload,
  MonitorPayload,
  Notification,
  NotificationLog,
  PublicSettings,
  SessionResponse,
  UptimeStats,
} from './types'

/** Core API client: bootstrap, authentication, monitors and notifications. */
export const coreApi = {
  settings: () => get<PublicSettings>('/api/settings'),
  session: () => get<SessionResponse>('/api/auth/session'),
  login: (email: string, password: string, recaptchaToken?: string) =>
    post<{ authenticated: boolean; identity: Identity }>('/api/auth/login', {
      email,
      password,
      recaptcha_token: recaptchaToken ?? '',
    }),
  logout: () => post<void>('/api/auth/logout'),
  oidcLoginURL: (redirect = '/') => buildURL('/api/auth/oidc/login', { redirect }),

  dashboard: (query?: Query) => get<DashboardResponse>('/api/dashboard', query),

  monitors: (query?: Query) => get<Monitor[]>('/api/monitors', query),
  monitor: (id: number) => get<Monitor>(`/api/monitors/${id}`),
  createMonitor: (payload: MonitorPayload) => post<Monitor>('/api/monitors', payload),
  updateMonitor: (id: number, payload: MonitorPayload) => put<Monitor>(`/api/monitors/${id}`, payload),
  deleteMonitor: (id: number) => del<void>(`/api/monitors/${id}`),
  pauseMonitor: (id: number) => post<Monitor>(`/api/monitors/${id}/pause`),
  resumeMonitor: (id: number) => post<Monitor>(`/api/monitors/${id}/resume`),
  checkMonitor: (id: number) => post<{ queued: boolean }>(`/api/monitors/${id}/check`),
  monitorHeartbeats: (id: number, query?: Query) => get<Heartbeat[]>(`/api/monitors/${id}/heartbeats`, query),
  monitorStats: (id: number, query?: Query) =>
    get<{ stats: UptimeStats; series: Heartbeat[] }>(`/api/monitors/${id}/stats`, query),
  cloneMonitor: (id: number, options: MonitorCloneOptions) =>
    post<Monitor>(`/api/monitors/${id}/clone`, options),

  monitorGroups: () => get<MonitorGroup[]>('/api/monitor-groups'),
  monitorGroup: (id: number) => get<MonitorGroup>(`/api/monitor-groups/${id}`),
  createMonitorGroup: (payload: MonitorGroupPayload) => post<MonitorGroup>('/api/monitor-groups', payload),
  updateMonitorGroup: (id: number, payload: MonitorGroupPayload) =>
    put<MonitorGroup>(`/api/monitor-groups/${id}`, payload),
  deleteMonitorGroup: (id: number) => del<void>(`/api/monitor-groups/${id}`),
  setMonitorGroupMonitors: (id: number, monitorIds: number[]) =>
    put<MonitorGroup>(`/api/monitor-groups/${id}/monitors`, { monitor_ids: monitorIds }),
  cloneMonitorGroup: (id: number, options: MonitorGroupCloneOptions) =>
    post<MonitorGroup>(`/api/monitor-groups/${id}/clone`, options),

  notifications: () => get<Notification[]>('/api/notifications'),
  notification: (id: number) => get<Notification>(`/api/notifications/${id}`),
  createNotification: (payload: Partial<Notification>) => post<Notification>('/api/notifications', payload),
  updateNotification: (id: number, payload: Partial<Notification>) =>
    put<Notification>(`/api/notifications/${id}`, payload),
  deleteNotification: (id: number) => del<void>(`/api/notifications/${id}`),
  testNotification: (id: number) => post<NotificationLog>(`/api/notifications/${id}/test`),
  notificationLogs: (query?: Query) => get<NotificationLog[]>('/api/notifications/logs', query),
}
