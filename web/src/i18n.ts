import { createI18n } from 'vue-i18n'
import enUS from '@/locales/en-US.json'
import ptBR from '@/locales/pt-BR.json'
import esMX from '@/locales/es-MX.json'

/** Languages shipped with the frontend (must match DEFAULT_LOCALE values). */
export const SUPPORTED_LOCALES = ['en-US', 'pt-BR', 'es-MX'] as const
export type SupportedLocale = (typeof SUPPORTED_LOCALES)[number]

export const LOCALE_STORAGE_KEY = 'up.locale'
export const THEME_STORAGE_KEY = 'up.theme'

export const i18n = createI18n({
  legacy: false,
  locale: 'en-US',
  fallbackLocale: 'en-US',
  globalInjection: true,
  messages: {
    'en-US': enUS,
    'pt-BR': ptBR,
    'es-MX': esMX,
  },
})

/** isSupportedLocale guards a value coming from localStorage or the API. */
export function isSupportedLocale(value: unknown): value is SupportedLocale {
  return typeof value === 'string' && (SUPPORTED_LOCALES as readonly string[]).includes(value)
}

/**
 * detectLocale picks the best language for a visitor:
 *  1. the value previously stored in the browser;
 *  2. the browser languages;
 *  3. the DEFAULT_LOCALE configured by the administrator.
 */
export function detectLocale(fallback: string): SupportedLocale {
  const stored = localStorage.getItem(LOCALE_STORAGE_KEY)
  if (isSupportedLocale(stored)) return stored

  const candidates = [navigator.language, ...(navigator.languages ?? [])].filter(Boolean) as string[]
  for (const candidate of candidates) {
    if (isSupportedLocale(candidate)) return candidate
    const base = candidate.split('-')[0]
    const match = SUPPORTED_LOCALES.find((locale) => locale.toLowerCase().startsWith(`${base.toLowerCase()}-`))
    if (match) return match
  }
  return isSupportedLocale(fallback) ? fallback : 'en-US'
}

/** Native name of a language, read from its own message bundle. */
export function localeLabel(locale: string): string {
  const messages = i18n.global.getLocaleMessage(locale) as { language?: string }
  return messages.language ?? locale
}
