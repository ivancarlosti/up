import type { MonitorConfig, MonitorPayload, MonitorType } from './types'

/**
 * Type aware pruning of a monitor payload.
 *
 * The API speaks one flat config struct plus a handful of top level switches
 * (the certificate and domain watches, the template link) and the form renders
 * the same object for every probe type: only the fields of the selected type
 * are shown, but the hidden ones keep the values of the type the monitor had.
 * Sending them back is what the backend rejects ("cert_watch is only available
 * for http, keyword and ssl monitors"), so every save goes through
 * `sanitizeMonitorPayload`, which drops what does not belong to the type the
 * operator selected.
 *
 * `CONFIG_FIELDS` mirrors the Go side (`internal/models/monitor_config.go`, the
 * `Validate` of every type in `internal/services/monitor_validate.go` and the
 * `Supports*` helpers in `internal/models/enums.go`): keep the two in sync when
 * a monitor type or one of its options is added (see `docs/architecture.md`).
 *
 * Fields shared by several types (`host`/`port` for TCP and SSL, `ignore_tls`
 * for HTTP, Keyword and SSL) are listed by each of them on purpose, so a switch
 * between those types does not lose the value the new one still uses.
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
  ],
  tcp: ['host', 'port', 'send', 'expect'],
  dns: ['hostname', 'resolver_server', 'record_type', 'expected_value', 'invert_check'],
  ssl: ['host', 'port', 'server_name', 'ignore_tls'],
}

/** supportsCertificate reports whether a type can read a TLS certificate. */
export function supportsCertificate(type: MonitorType): boolean {
  return type === 'http' || type === 'keyword' || type === 'ssl'
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
      return true
  }
  return false
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

  // The domain watch is available everywhere, but nothing can outlive it.
  if (!clean.domain_watch) {
    clean.domain_notify = false
    clean.domain_warn_days = ''
    clean.domain_expires_at = null
  }

  if (clean.template_uuid && options.templateType && options.templateType !== type) {
    clean.template_uuid = ''
  }

  return clean
}
