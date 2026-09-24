import { formatDateTime } from './format'
import type { CertificateInfo, DomainInfo } from './types'

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

/** domainTitle is the tooltip of a domain badge (registrar + expiry). */
export function domainTitle(domain: DomainInfo, locale: string): string {
  const parts = [domain.registrar, domain.domain].filter(Boolean)
  return `${parts.join(' — ')} (${formatDateTime(domain.expires_at, locale)})`
}
