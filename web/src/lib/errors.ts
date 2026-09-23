import { APIError } from '@/lib/api'
import { i18n } from '@/i18n'

/**
 * Codes whose backend message explains *what* has to be fixed. The translated
 * sentence is generic ("Invalid request body"), which is useless when the body
 * was rejected because a single field had the wrong type, so the detail is
 * appended for these codes only.
 */
const EXPLANATORY_CODES = new Set([
  'ERR_INVALID_PAYLOAD',
  'ERR_VALIDATION',
  'ERR_NOTIFICATION_CONFIG_INVALID',
  'ERR_MONITOR_CONFIG_INVALID',
])

/** detail strips the prefix that the translated sentence already carries. */
function detail(message: string): string {
  return (message ?? '').replace(/^invalid request body:\s*/i, '').trim()
}

/**
 * translateError converts a backend error into a localized message.
 *
 * The Go API always answers with a stable code (ERR_MONITOR_NOT_FOUND, ...) and
 * the frontend owns the translations: when the code is unknown the raw message
 * is shown instead, which keeps developer information available.
 */
export function translateError(error: unknown): string {
  if (error instanceof APIError) {
    const key = `errors.${error.code}`
    const translate = i18n.global.t as (key: string) => string
    const hasKey = i18n.global.te ? i18n.global.te(key) : false
    const base = hasKey ? translate(key) : error.message || translate('errors.ERR_INTERNAL')
    const extra = EXPLANATORY_CODES.has(error.code) ? detail(error.message) : ''
    return extra && !base.includes(extra) ? `${base} — ${extra}` : base
  }
  if (error instanceof Error) return error.message
  return String(error)
}

/** translatedErrorCode returns the code itself (useful for logging/debug). */
export function errorCode(error: unknown): string {
  return error instanceof APIError ? error.code : 'ERR_INTERNAL'
}
