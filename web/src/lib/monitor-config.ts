import type {
  LighthouseInterval,
  LighthouseResult,
  LighthouseRunStrategy,
  LighthouseStrategy,
  LighthouseWeekday,
  Monitor,
  MonitorConfig,
  MonitorPayload,
  MonitorType,
  TemplateDefaults,
} from './types'

/**
 * Type aware pruning of a monitor payload.
 *
 * The API speaks one flat config struct plus a handful of top level switches
 * (the certificate and domain watches, the template link) and the form renders
 * the same object for every probe type: only the fields of the selected type
 * are shown, but the hidden ones keep the values of the type the monitor had.
 * Sending them back is what the backend rejects ("cert_watch is only available
 * for http, keyword, ssl and websocket monitors"), so every save goes through
 * `sanitizeMonitorPayload`, which drops what does not belong to the type the
 * operator selected.
 *
 * `CONFIG_FIELDS` mirrors the Go side (`internal/models/monitor_config.go`, the
 * `Validate` of every type in `internal/services/monitor_validate.go` and the
 * `Supports*` helpers in `internal/models/enums.go`): keep the two in sync when
 * a monitor type or one of its options is added (see `docs/architecture.md`).
 *
 * Fields shared by several types (`host`/`port` for TCP and SSL, `ignore_tls`,
 * the headers and the credentials for HTTP, Keyword and WebSocket, `send`/`expect`
 * for TCP and WebSocket, `ip_family` for every type) are listed by each of them on
 * purpose, so a switch between those types does not lose the value the new one
 * still uses.
 */
export const CONFIG_FIELDS: Record<MonitorType, readonly (keyof MonitorConfig)[]> = {
  http: [
    'url',
    'method',
    'encoding',
    'body',
    'headers',
    'auth_type',
    'basic_user',
    'basic_pass',
    'bearer_token',
    'ignore_tls',
    'max_redirects',
    'cache_buster',
    'accepted_status_codes',
    'ip_family',
    // The strategy and the cadence of the Lighthouse run (see
    // `supportsLighthouse`): they travel with the two HTTP(s) types, and the
    // switch itself is a top level field.
    'lighthouse_strategy',
    'lighthouse_interval',
  ],
  keyword: [
    'url',
    'method',
    'encoding',
    'body',
    'headers',
    'auth_type',
    'basic_user',
    'basic_pass',
    'bearer_token',
    'ignore_tls',
    'max_redirects',
    'cache_buster',
    'accepted_status_codes',
    'keyword',
    'invert_keyword',
    'case_sensitive',
    'ip_family',
    'lighthouse_strategy',
    'lighthouse_interval',
  ],
  tcp: ['host', 'port', 'send', 'expect', 'ip_family'],
  dns: ['hostname', 'resolver_server', 'record_type', 'expected_value', 'invert_check', 'ip_family'],
  ssl: ['host', 'port', 'server_name', 'ignore_tls', 'ip_family'],
  // The upgrade is an HTTP request: the url, the headers and the credentials
  // travel with it, plus the optional frame exchange of a tcp monitor.
  websocket: [
    'url',
    'headers',
    'auth_type',
    'basic_user',
    'basic_pass',
    'bearer_token',
    'ignore_tls',
    'send',
    'expect',
    'ip_family',
  ],
}

/** supportsCertificate reports whether a type can read a TLS certificate. */
export function supportsCertificate(type: MonitorType): boolean {
  return type === 'http' || type === 'keyword' || type === 'ssl' || type === 'websocket'
}

/**
 * supportsDomainWatch reports whether a type has a target a registrable domain
 * can be derived from. Every current type does; the switch exists because the
 * backend has the same one (`MonitorType.SupportsDomainWatch`).
 */
export function supportsDomainWatch(type: MonitorType): boolean {
  switch (type) {
    case 'http':
    case 'keyword':
    case 'tcp':
    case 'dns':
    case 'ssl':
    case 'websocket':
      return true
  }
  return false
}

/**
 * supportsLighthouse reports whether a type has a page Google Lighthouse can
 * audit through the PageSpeed Insights API. Only the two HTTP(s) types do; the
 * mirror of the Go helper `MonitorType.SupportsLighthouse`.
 */
export function supportsLighthouse(type: MonitorType): boolean {
  return type === 'http' || type === 'keyword'
}

/**
 * The strategies a monitor (or the job) can be configured with (mirror of
 * `models.LighthouseStrategies`): a single run, or `both` of them. `both` costs
 * two PageSpeed Insights calls per measurement.
 */
export const LIGHTHOUSE_STRATEGIES: readonly LighthouseStrategy[] = ['mobile', 'desktop', 'both']

/** The strategy a monitor measures with when it does not pick one. */
export const DEFAULT_LIGHTHOUSE_STRATEGY: LighthouseStrategy = 'mobile'

/**
 * The runs a strategy stands for (mirror of `models.LighthouseStrategyTargets`):
 * one run, or the two of them for `both`, mobile first. It is what tells the UI
 * how many score blocks a monitor has.
 */
export const LIGHTHOUSE_STRATEGY_TARGETS: Record<LighthouseStrategy, readonly LighthouseRunStrategy[]> = {
  mobile: ['mobile'],
  desktop: ['desktop'],
  both: ['mobile', 'desktop'],
}

/**
 * The i18n key of each strategy label: the pickers and the score badges all read
 * it, so a strategy cannot be labelled one way in the form and another in the
 * table.
 */
export const LIGHTHOUSE_STRATEGY_KEYS: Record<LighthouseStrategy, string> = {
  mobile: 'monitor.lighthouseStrategyMobile',
  desktop: 'monitor.lighthouseStrategyDesktop',
  both: 'monitor.lighthouseStrategyBoth',
}

/**
 * lighthouseStrategy normalizes a stored strategy: an unknown value reads as the
 * default run, like `models.NormalizeLighthouseStrategy` on the Go side.
 */
export function lighthouseStrategy(value?: string | null): LighthouseStrategy {
  const raw = (value ?? '').trim().toLowerCase()
  const found = LIGHTHOUSE_STRATEGIES.find((strategy) => strategy === raw)
  return found ?? DEFAULT_LIGHTHOUSE_STRATEGY
}

/**
 * lighthouseResults lists the observations of a monitor, one per run it measures
 * with (a monitor set to `both` has two, mobile first), which is what the
 * listings and the monitor detail render.
 *
 * A payload without `lighthouses` (an older node, a client that only knows the
 * single field) falls back to `lighthouse`, so a stored score is never hidden.
 */
export function lighthouseResults(
  monitor: Pick<Monitor, 'config' | 'lighthouse' | 'lighthouses'>,
): LighthouseResult[] {
  const stored = monitor.lighthouses ?? {}
  const results = LIGHTHOUSE_STRATEGY_TARGETS[lighthouseStrategy(monitor.config?.lighthouse_strategy)]
    .map((run) => stored[run])
    .filter((result): result is LighthouseResult => Boolean(result))
  if (results.length > 0) return results
  return monitor.lighthouse ? [monitor.lighthouse] : []
}

/** The cadences the settings page and the monitor form offer (mirror of `models.LighthouseIntervals`). */
export const LIGHTHOUSE_INTERVALS: readonly LighthouseInterval[] = ['hourly', '6h', '12h', 'daily', 'weekly']

/** The cadence a monitor measures with when it does not pick one. */
export const DEFAULT_LIGHTHOUSE_INTERVAL: LighthouseInterval = 'weekly'

/** The weekdays the settings page offers (mirror of `models.LighthouseWeekdays`). */
export const LIGHTHOUSE_WEEKDAYS: readonly LighthouseWeekday[] = [
  'sunday',
  'monday',
  'tuesday',
  'wednesday',
  'thursday',
  'friday',
  'saturday',
]

/** The weekday a weekly cadence is anchored to when the operator does not pick one. */
export const DEFAULT_LIGHTHOUSE_WEEKDAY: LighthouseWeekday = 'monday'

/**
 * lighthouseInterval normalizes a stored cadence: anything unknown reads as the
 * default one, like `models.NormalizeLighthouseInterval` on the Go side. An
 * absent value is not the default though (a monitor without a cadence follows the
 * job setting): use `effectiveLighthouseInterval` for that.
 */
export function lighthouseInterval(value?: string | null): LighthouseInterval {
  const found = LIGHTHOUSE_INTERVALS.find((interval) => interval === value)
  return found ?? DEFAULT_LIGHTHOUSE_INTERVAL
}

/**
 * effectiveLighthouseInterval resolves the cadence a monitor really measures
 * with: its own, or the job setting when it carries none (a monitor stored before
 * the field existed). It is the twin of `lighthouse.IntervalFor` on the Go side.
 */
export function effectiveLighthouseInterval(
  monitor: Pick<Monitor, 'config'>,
  fallback?: string | null,
): LighthouseInterval {
  const own = monitor.config?.lighthouse_interval
  if (own && LIGHTHOUSE_INTERVALS.includes(own)) return own
  return lighthouseInterval(fallback)
}

/** lighthouseWeekday normalizes a stored weekday, defaulting to Monday. */
export function lighthouseWeekday(value?: string | null): LighthouseWeekday {
  const found = LIGHTHOUSE_WEEKDAYS.find((weekday) => weekday === value)
  return found ?? DEFAULT_LIGHTHOUSE_WEEKDAY
}

/**
 * The i18n key of each cadence: the options are translated, the stored value is
 * never shown (it is a code like `6h`, not a label).
 */
export const LIGHTHOUSE_INTERVAL_KEYS: Record<LighthouseInterval, string> = {
  hourly: 'lighthouse.intervalHourly',
  '6h': 'lighthouse.intervalEvery6h',
  '12h': 'lighthouse.intervalEvery12h',
  daily: 'lighthouse.intervalDaily',
  weekly: 'lighthouse.intervalWeekly',
}

/** The i18n key of each weekday. */
export const LIGHTHOUSE_WEEKDAY_KEYS: Record<LighthouseWeekday, string> = {
  sunday: 'lighthouse.weekdaySunday',
  monday: 'lighthouse.weekdayMonday',
  tuesday: 'lighthouse.weekdayTuesday',
  wednesday: 'lighthouse.weekdayWednesday',
  thursday: 'lighthouse.weekdayThursday',
  friday: 'lighthouse.weekdayFriday',
  saturday: 'lighthouse.weekdaySaturday',
}

/** pruneMonitorConfig keeps only the probe options the type actually reads. */
export function pruneMonitorConfig(type: MonitorType, config?: MonitorConfig | null): MonitorConfig {
  const source = config ?? {}
  const pruned: Record<string, unknown> = {}
  for (const field of CONFIG_FIELDS[type]) {
    const value = source[field]
    if (value === undefined || value === null) continue
    pruned[field] = value
  }
  return pruned as MonitorConfig
}

/**
 * stripMonitorAuth returns a copy of a probe configuration without the HTTP
 * authentication options.
 *
 * Authentication is a property of the MONITOR, never of a template: two monitors
 * can follow the same template against the same host with different credentials
 * (or none), so the template dialog renders the probe fields without the auth
 * inputs (`MonitorConfigFields` `withAuth`) and saves the configuration through
 * this helper.
 *
 * It is the TypeScript twin of `models.MonitorConfig.WithoutAuth`, which strips
 * the same four fields on every template write: the UI is not the guarantee, it
 * is the first of the two.
 */
export function stripMonitorAuth(config?: MonitorConfig | null): MonitorConfig {
  const clean: MonitorConfig = { ...(config ?? {}) }
  clean.auth_type = 'none'
  delete clean.basic_user
  delete clean.basic_pass
  delete clean.bearer_token
  return clean
}

/** What the sanitizer needs to know about the template the monitor follows. */
export interface SanitizeOptions {
  /**
   * Type of the linked template, when the caller has the template list. A link
   * that points at a template of another type is not a template this monitor
   * can follow, so it is dropped instead of failing the save.
   */
  templateType?: MonitorType | null
}

/**
 * sanitizeMonitorPayload returns a copy of the payload without the fields that
 * do not apply to the selected type: the probe options, the certificate
 * switches and the template link. The form can keep those values hidden while
 * the operator experiments with the type; they never reach the API.
 */
export function sanitizeMonitorPayload(payload: MonitorPayload, options: SanitizeOptions = {}): MonitorPayload {
  const clean: MonitorPayload = { ...payload }
  const type = (clean.type ?? 'http') as MonitorType

  clean.config = pruneMonitorConfig(type, clean.config)

  // The certificate switches only exist for the types that can read a
  // certificate, and notifying about a watch that is off is a silent no-op.
  if (!supportsCertificate(type) || !clean.cert_watch) {
    clean.cert_watch = false
    clean.cert_notify = false
    clean.cert_warn_days = ''
  }

  // The domain watch is available everywhere, but nothing can outlive it —
  // except the manual date, which belongs to the DOMAIN: the server mirrors it
  // to every monitor of the domain, so clearing it here just because the switch
  // is off would take the date away from all of them.
  if (!clean.domain_watch) {
    clean.domain_notify = false
    clean.domain_warn_days = ''
  }

  // The web performance switch only exists where PageSpeed Insights can audit a
  // page, and the strategy and the cadence do not outlive it.
  if (!supportsLighthouse(type) || !clean.lighthouse_enabled) {
    clean.lighthouse_enabled = false
    if (clean.config) {
      delete clean.config.lighthouse_strategy
      delete clean.config.lighthouse_interval
    }
  }

  if (clean.template_uuid && options.templateType && options.templateType !== type) {
    clean.template_uuid = ''
  }

  return clean
}

/**
 * sanitizeTemplateDefaults returns a copy of a template's defaults without the
 * switches the type cannot honour, mirroring `sanitizeMonitorPayload` and the Go
 * side (`models.MonitorTemplate.Validate`).
 *
 * A template is a monitor without a target: the certificate switches only exist
 * for the types that can read a certificate (http, keyword, ssl and websocket)
 * and nothing can outlive the watch it belongs to. Without this, a certificate
 * watch kept while the operator tried another type would be stored on, say, a
 * tcp template and every monitor created from it would be rejected by the API.
 */
export function sanitizeTemplateDefaults(type: MonitorType, defaults: TemplateDefaults): TemplateDefaults {
  const clean: TemplateDefaults = { ...defaults }

  if (!supportsCertificate(type) || !clean.cert_watch) {
    clean.cert_watch = false
    clean.cert_notify = false
    clean.cert_warn_days = ''
  }

  if (!supportsDomainWatch(type) || !clean.domain_watch) {
    clean.domain_watch = false
    clean.domain_notify = false
    clean.domain_warn_days = ''
  }

  if (!supportsLighthouse(type) || !clean.lighthouse_enabled) {
    clean.lighthouse_enabled = false
  }

  return clean
}

/** Port a ssl monitor dials when none is configured (mirror of `models.DefaultSSLPort`). */
const DEFAULT_SSL_PORT = 443

/**
 * monitorTarget renders the probe target in a readable form, exactly like
 * `notify.MonitorTarget` on the Go side: the URL of an http/keyword/websocket
 * probe, the `host:port` of tcp, the `record host @resolver` of dns and the
 * `host:port` of ssl (falling back to 443). It is what the table prints under the
 * monitor name.
 */
export function monitorTarget(monitor: Monitor): string {
  const config = monitor.config ?? {}
  switch (monitor.type) {
    case 'http':
    case 'keyword':
    case 'websocket':
      return config.url ?? ''
    case 'tcp':
      return `${config.host ?? ''}:${config.port ?? 0}`
    case 'dns':
      return `${config.record_type ?? ''} ${config.hostname ?? ''} @${config.resolver_server ?? ''}`
    case 'ssl':
      return `${config.host ?? ''}:${config.port && config.port > 0 ? config.port : DEFAULT_SSL_PORT}`
  }
  return monitor.type
}

/**
 * monitorTargetURL returns the target when it is a web URL that can be opened,
 * so the table makes it a link; every other target (a host, a DNS record) is
 * plain text. A malformed or non http(s) URL is not a link.
 */
export function monitorTargetURL(monitor: Monitor): string | null {
  if (monitor.type !== 'http' && monitor.type !== 'keyword') return null
  const url = monitor.config?.url?.trim()
  if (!url) return null
  try {
    const parsed = new URL(url)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null
  } catch {
    return null
  }
  return url
}
