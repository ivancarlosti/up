import { formatDateTime } from './format'
import type { CertificateInfo, DomainInfo, LighthouseResult } from './types'

/**
 * Shared rendering rules of the expiration watches.
 *
 * The certificate and the domain badge appear on the dashboard card, the monitor
 * detail page, the admin table and the admin expiry page: one module owns the
 * colour and the tooltip so the four surfaces cannot disagree.
 */

/** Badge variants used by the expiry badges (mirrors Badge.vue). */
export type ExpiryBadgeVariant = 'success' | 'warning' | 'danger' | 'secondary'

/** expiryVariant colours a badge by urgency: 7 days or less is an emergency. */
export function expiryVariant(daysLeft: number): ExpiryBadgeVariant {
  if (daysLeft <= 7) return 'danger'
  if (daysLeft <= 30) return 'warning'
  return 'success'
}

/**
 * expiryStateVariant colours a badge that may not have an observation yet: a
 * status other than "ok" (or a missing date) is shown in the neutral variant
 * instead of pretending to be a healthy number.
 */
export function expiryStateVariant(status?: string, daysLeft?: number): ExpiryBadgeVariant {
  if (status !== 'ok' || typeof daysLeft !== 'number') return 'secondary'
  return expiryVariant(daysLeft)
}

/** certificateTitle is the tooltip of a certificate badge (issuer + expiry). */
export function certificateTitle(certificate: CertificateInfo, locale: string): string {
  const parts = [certificate.issuer, certificate.subject].filter(Boolean)
  return `${parts.join(' — ')} (${formatDateTime(certificate.not_after, locale)})`
}

/** domainTitle is the tooltip of a domain badge (registrar + expiry + registry status). */
export function domainTitle(domain: DomainInfo, locale: string): string {
  const parts = [domain.registrar, domain.domain].filter(Boolean)
  const statuses = (domain.rdap_status ?? []).filter(Boolean)
  const status = statuses.length > 0 ? ` [${statuses.join(', ')}]` : ''
  return `${parts.join(' — ')} (${formatDateTime(domain.expires_at, locale)})${status}`
}

/**
 * lighthouseVariant colours the web performance badge by the score band: the
 * same thresholds the Lighthouse report uses (90 good, 50 average, below poor).
 * A run that failed (or a score the API could not measure) is shown in the
 * neutral variant instead of pretending to be a score.
 */
export function lighthouseVariant(result: LighthouseResult): ExpiryBadgeVariant {
  if (result.status !== 'ok' || result.performance === null || result.performance === undefined) {
    return 'secondary'
  }
  if (result.performance >= 90) return 'success'
  if (result.performance >= 50) return 'warning'
  return 'danger'
}

/** lighthouseTitle is the tooltip of a score badge (strategy, scores, when). */
export function lighthouseTitle(result: LighthouseResult, locale: string): string {
  if (result.status !== 'ok') return result.message || 'Lighthouse'
  const score = (value: number | null) => (value === null || value === undefined ? '—' : String(value))
  return [
    result.strategy,
    `${score(result.performance)} / ${score(result.accessibility)}`,
    `${score(result.best_practices)} / ${score(result.seo)}`,
    formatDateTime(result.ran_at, locale),
  ].join(' — ')
}
