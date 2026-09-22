import { APIError } from '@/lib/api'
import { i18n } from '@/i18n'

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
    if (hasKey) return translate(key)
    return error.message || translate('errors.ERR_INTERNAL')
  }
  if (error instanceof Error) return error.message
  return String(error)
}

/** translatedErrorCode returns the code itself (useful for logging/debug). */
export function errorCode(error: unknown): string {
  return error instanceof APIError ? error.code : 'ERR_INTERNAL'
}
