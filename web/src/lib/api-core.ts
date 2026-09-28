import { buildURL, del, get, post, put } from './http'
import type { Query } from './http'
import type {
  ApplyResult,
  ApplyTemplateOptions,
  BulkOptions,
  BulkReport,
  BulkTagOptions,
  BulkTagReport,
  DashboardResponse,
  Heartbeat,
  Identity,
  Monitor,
  MonitorCloneOptions,
  MonitorGroup,
  MonitorGroupCloneOptions,
  MonitorGroupPayload,
  MonitorPayload,
  MonitorTemplate,
  MonitorTemplatePayload,
  MonitorType,
  TagUsage,
  TemplateLinkResult,
  TemplateLinkScope,
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
  /**
   * oidcLogoutURL clears the local session and then hands the browser over to
   * the identity provider, which drops its own session as well. Without it the
   * next "Sign in with Keycloak" would silently reuse the same account.
   */
  oidcLogoutURL: (redirect = '/login') => buildURL('/api/auth/oidc/logout', { redirect }),

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

  monitorTemplates: () => get<MonitorTemplate[]>('/api/monitor-templates'),
  monitorTemplate: (id: number) => get<MonitorTemplate>(`/api/monitor-templates/${id}`),
  createMonitorTemplate: (payload: MonitorTemplatePayload) => post<MonitorTemplate>('/api/monitor-templates', payload),
  updateMonitorTemplate: (id: number, payload: MonitorTemplatePayload) =>
    put<MonitorTemplate>(`/api/monitor-templates/${id}`, payload),
  deleteMonitorTemplate: (id: number) => del<void>(`/api/monitor-templates/${id}`),
  applyMonitorTemplate: (id: number, options: ApplyTemplateOptions) =>
    post<{ dry_run: boolean; results: ApplyResult[] }>(`/api/monitor-templates/${id}/apply`, options),
  /**
   * linkAllMonitorTemplate attaches the monitors in scope to the template.
   *
   * `scope` is the selection of the run: every monitor of the type, some groups
   * (by uuid) or some tags. It is remembered on the template, so the dialog
   * reopens on it. A scope with an outside (groups, tags) also detaches the
   * followers of the template that fall outside the selection.
   */
  linkAllMonitorTemplate: (id: number, dryRun = false, scope?: TemplateLinkScope) =>
    post<TemplateLinkResult>(`/api/monitor-templates/${id}/link-all`, { dry_run: dryRun, scope }),
  /**
   * monitorTags returns the tags in use with the number of monitors carrying
   * each of them, optionally narrowed to one monitor type. It is the vocabulary
   * the tag pickers offer (tags are a free form list on the monitor).
   */
  monitorTags: (type?: MonitorType) => get<TagUsage[]>('/api/monitors/tags', type ? { type } : undefined),
  bulkCreateMonitors: (options: BulkOptions) => post<BulkReport>('/api/monitors/bulk', options),
  /**
   * bulkUpdateMonitorTags adds and removes tags on a set of monitors. The
   * selection is the union of the ids, the groups and the tag it names, and
   * `dry_run` previews the before/after of every row without writing.
   */
  bulkUpdateMonitorTags: (options: BulkTagOptions) =>
    post<BulkTagReport>('/api/monitors/bulk/tags', options),

  notifications: () => get<Notification[]>('/api/notifications'),
  notification: (id: number) => get<Notification>(`/api/notifications/${id}`),
  createNotification: (payload: Partial<Notification>) => post<Notification>('/api/notifications', payload),
  updateNotification: (id: number, payload: Partial<Notification>) =>
    put<Notification>(`/api/notifications/${id}`, payload),
  deleteNotification: (id: number) => del<void>(`/api/notifications/${id}`),
  testNotification: (id: number) => post<NotificationLog>(`/api/notifications/${id}/test`),
  notificationLogs: (query?: Query) => get<NotificationLog[]>('/api/notifications/logs', query),
}
