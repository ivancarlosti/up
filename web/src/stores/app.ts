import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { api } from '@/lib/api'
import { APIError } from '@/lib/http'
import { applyDocumentDirection, applyPreferredLocale, detectLocale, i18n, isSupportedLocale, LOCALE_STORAGE_KEY } from '@/i18n'
import { hour12FromSetting, setHour12Preference } from '@/lib/format'
import { useThemeStore } from '@/stores/theme'
import type { BootStatus, Identity, PublicSettings, SessionResponse } from '@/lib/types'

/**
 * Boot codes the backend answers with while it is not ready (see internal/boot
 * and internal/i18n/errors.go). They are the ONLY reason the SPA shows the boot
 * screen: a network failure (a proxy, a stopped server, `npm run smoke` without
 * a backend) must keep the normal routing, or the login page would become
 * unreachable in the only case where it is still useful.
 */
function isBootCode(code: string): boolean {
  return code.startsWith('ERR_BOOT_') || code.startsWith('ERR_DB_')
}

/**
 * isBootUnavailable reports whether a failure means "the API answers but is not
 * ready yet". It requires a typed APIError: a fetch that never reached the
 * server (TypeError) is not a boot problem.
 */
export function isBootUnavailable(error: unknown): boolean {
  return error instanceof APIError && error.status === 503 && isBootCode(error.code)
}

/** How often the boot screen refreshes while it waits (then it slows down). */
const BOOT_POLL_MS = 2000
const BOOT_POLL_SLOW_MS = 5000
/** How long the fast interval lasts before switching to the slow one. */
const BOOT_POLL_FAST_WINDOW_MS = 60_000
/** How often a serving instance checks that the database is still there. */
const HEALTH_POLL_MS = 30_000

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

  /**
   * bootPending is true while the backend is starting, or for as long as its
   * database stays broken: the shell shows the boot screen instead of the router
   * outlet, and every navigation waits for the API (see waitForBoot).
   */
  const bootPending = ref(false)
  /**
   * boot is the last status read from the API. It stays live after the boot,
   * which is what reports a database that died while the process was serving.
   */
  const boot = ref<BootStatus | null>(null)
  /** True while the API answers but cannot do its job (the database is gone). */
  const degraded = computed(() => boot.value?.status === 'degraded')

  const theme = useThemeStore()

  const locale = computed(() => i18n.global.locale.value)
  const authenticated = computed(() => session.value?.authenticated ?? false)
  const authMethod = computed(() => settings.value?.auth_method ?? 'account')
  const appName = computed(() => settings.value?.app_name ?? 'Up')

  /**
   * bootstrap loads the public settings and the session, then applies the
   * defaults configured by the administrator.
   *
   * A 503 carrying a boot code is not an error: it means the backend is up and
   * still preparing its database, so the store switches to the boot screen
   * instead of reporting a dead API.
   */
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

      bootPending.value = false
      pollStarted = 0
      // An instance that is serving must still notice that its database went
      // away: no single request would (every read is short lived).
      schedulePoll(HEALTH_POLL_MS)
    } catch (error) {
      if (isBootUnavailable(error)) {
        // The boot screen is about to be the only thing on screen and the API
        // that reports DEFAULT_LOCALE cannot answer: use the stored language.
        applyPreferredLocale()
        startBootWatch()
      } else {
        fatalError.value = error instanceof Error ? error.message : String(error)
      }
    } finally {
      loading.value = false
    }
  }

  // --- boot watching -----------------------------------------------------
  let pollTimer: ReturnType<typeof setTimeout> | null = null
  let pollStarted = 0
  // waiting parks the navigations that arrived while the API was not ready;
  // resolveWaiting releases them the moment it is.
  let waiting: Promise<void> | null = null
  let resolveWaiting: (() => void) | null = null

  /**
   * waitForBoot resolves once the API is serving again.
   *
   * The router guard awaits it, so the visitor keeps the URL they asked for
   * (including the ?redirect= of a deep link) while the boot screen is on and
   * the camera keeps rolling - instead of being redirected to the login page of
   * an API that cannot answer yet.
   */
  function waitForBoot(): Promise<void> {
    if (!bootPending.value) return Promise.resolve()
    if (!waiting) {
      waiting = new Promise<void>((resolve) => {
        resolveWaiting = resolve
      })
    }
    return waiting
  }

  /** releaseWaiters resumes every parked navigation. */
  function releaseWaiters(): void {
    resolveWaiting?.()
    resolveWaiting = null
    waiting = null
  }

  /** readBoot refreshes `boot` from /api/boot; it reports whether the API answered. */
  async function readBoot(): Promise<boolean> {
    try {
      boot.value = await api.boot()
      return true
    } catch {
      // The process is behind a proxy that is down, or it is restarting: the
      // screen already shows the last known state, so keep the timer running.
      return false
    }
  }

  /**
   * readHealth refreshes `boot` from /api/health, the live probe of an instance
   * that is already serving.
   *
   * It is the only thing that notices a database that went away after the boot:
   * /api/boot reports the state of the process, and nothing in the process is
   * writing to it while it serves, so the ping of /api/health is what turns a
   * broken connection into the banner of the shell.
   */
  async function readHealth(): Promise<void> {
    try {
      boot.value = await api.health()
    } catch (error) {
      if (!(error instanceof APIError) || error.status !== 503) return // a network blip, not a database problem
      boot.value = {
        status: 'degraded',
        ready: true,
        phase: boot.value?.phase ?? 'ready',
        database: 'error',
        code: error.code,
        detail: error.message,
      }
    }
  }

  /** bootInterval is fast during the first minute, then gentle. */
  function bootInterval(): number {
    return Date.now() - pollStarted < BOOT_POLL_FAST_WINDOW_MS ? BOOT_POLL_MS : BOOT_POLL_SLOW_MS
  }

  /** schedulePoll starts the single timer that reads the status. */
  function schedulePoll(delay: number): void {
    if (pollTimer !== null) return
    pollTimer = setTimeout(() => void pollStatus(), delay)
  }

  function stopPolling(): void {
    if (pollTimer === null) return
    clearTimeout(pollTimer)
    pollTimer = null
  }

  /**
   * startBootWatch shows the boot screen and polls until the API is ready.
   *
   * The delay starts at two seconds (a start is usually a few hundred
   * milliseconds, so the screen flips quickly) and drops to five once the wait
   * is clearly a failure an operator is working on.
   */
  function startBootWatch(): void {
    bootPending.value = true
    if (pollStarted === 0) pollStarted = Date.now()
    // Read right away instead of waiting for the first interval: the screen
    // knows the phase and the failure from its first paint.
    stopPolling()
    void pollStatus()
  }

  /** retryBoot asks again right now (the "try now" button of the boot screen). */
  function retryBoot(): void {
    if (!bootPending.value) return
    stopPolling()
    void pollStatus()
  }

  /**
   * pollStatus reads the status once and reschedules itself: /api/boot while the
   * backend is starting, /api/health once it is serving.
   */
  async function pollStatus(): Promise<void> {
    pollTimer = null
    if (!bootPending.value) {
      await readHealth()
      schedulePoll(HEALTH_POLL_MS)
      return
    }
    const answered = await readBoot()
    if (answered && boot.value?.ready) {
      bootPending.value = false
      // The navigation that was waiting resumes with the settings and the
      // session loaded; bootstrap reschedules if it fails again.
      releaseWaiters()
      await bootstrap()
    }
    schedulePoll(bootPending.value ? bootInterval() : HEALTH_POLL_MS)
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
    boot,
    bootPending,
    degraded,
    locale,
    authenticated,
    authMethod,
    appName,
    bootstrap,
    waitForBoot,
    retryBoot,
    refreshSession,
    setLocale,
    setTheme,
    signOut,
  }
})
