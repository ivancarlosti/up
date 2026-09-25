import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/lib/api'
import { applyDocumentDirection, detectLocale, i18n, isSupportedLocale, LOCALE_STORAGE_KEY } from '@/i18n'
import { hour12FromSetting, setHour12Preference } from '@/lib/format'
import { useThemeStore } from '@/stores/theme'
import type { Identity, PublicSettings, SessionResponse } from '@/lib/types'

/**
 * useAppStore holds the bootstrap state: public settings, the authenticated
 * identity, the language and the theme.
 */
export const useAppStore = defineStore('app', () => {
  const settings = ref<PublicSettings | null>(null)
  const session = ref<SessionResponse | null>(null)
  const identity = ref<Identity | null>(null)
  const loading = ref(true)
  const fatalError = ref<string | null>(null)

  const theme = useThemeStore()

  const locale = computed(() => i18n.global.locale.value)
  const authenticated = computed(() => session.value?.authenticated ?? false)
  const authMethod = computed(() => settings.value?.auth_method ?? 'account')
  const appName = computed(() => settings.value?.app_name ?? 'Up')

  /** bootstrap loads the public settings and the session, then applies the
   *  defaults configured by the administrator. */
  async function bootstrap(): Promise<void> {
    loading.value = true
    fatalError.value = null
    try {
      const [publicSettings, currentSession] = await Promise.all([api.settings(), api.session()])
      settings.value = publicSettings
      session.value = currentSession
      identity.value = currentSession.identity ?? null

      setLocale(detectLocale(publicSettings.default_locale ?? 'en-US'))
      theme.applyDefault(publicSettings.default_theme ?? 'system')
      setHour12Preference(hour12FromSetting(publicSettings.time_format))
    } catch (error) {
      fatalError.value = error instanceof Error ? error.message : String(error)
    } finally {
      loading.value = false
    }
  }

  /** refreshSession re-reads the session (used after login/logout). */
  async function refreshSession(): Promise<void> {
    const current = await api.session()
    session.value = current
    identity.value = current.identity ?? null
  }

  /** setLocale changes the language and persists the choice. */
  function setLocale(value: string): void {
    if (!isSupportedLocale(value)) return
    i18n.global.locale.value = value
    localStorage.setItem(LOCALE_STORAGE_KEY, value)
    document.documentElement.lang = value
    applyDocumentDirection(value)
  }

  /** setTheme changes the theme and persists the choice. */
  function setTheme(value: 'system' | 'light' | 'dark'): void {
    theme.set(value)
  }

  /** signOut clears the session cookie on the server and locally. */
  async function signOut(): Promise<void> {
    try {
      await api.logout()
    } finally {
      identity.value = null
      session.value = { ...(session.value as SessionResponse), authenticated: false }
    }
  }

  return {
    settings,
    session,
    identity,
    loading,
    fatalError,
    locale,
    authenticated,
    authMethod,
    appName,
    bootstrap,
    refreshSession,
    setLocale,
    setTheme,
    signOut,
  }
})
