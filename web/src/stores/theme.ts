import { defineStore } from 'pinia'
import { ref } from 'vue'
import { THEME_STORAGE_KEY } from '@/i18n'
import type { ThemeMode } from '@/lib/types'

/**
 * useThemeStore owns the light / dark / system preference.
 *
 * The choice is persisted in localStorage and the resolved mode is written to
 * the `dark` class of <html>, which is what Tailwind's dark variant reacts to.
 * The same script runs inline in index.html to avoid a flash of the wrong theme
 * before the application boots.
 */
export const useThemeStore = defineStore('theme', () => {
  const mode = ref<ThemeMode>(readStoredMode())
  const resolved = ref<'light' | 'dark'>(resolve(mode.value))

  const media = window.matchMedia('(prefers-color-scheme: dark)')
  media.addEventListener('change', () => {
    if (mode.value === 'system') apply()
  })

  function readStoredMode(): ThemeMode {
    const stored = localStorage.getItem(THEME_STORAGE_KEY)
    return stored === 'light' || stored === 'dark' || stored === 'system' ? stored : 'system'
  }

  function resolve(value: ThemeMode): 'light' | 'dark' {
    if (value === 'system') return media.matches ? 'dark' : 'light'
    return value
  }

  /** apply writes the current preference to the document. */
  function apply(): void {
    resolved.value = resolve(mode.value)
    document.documentElement.classList.toggle('dark', resolved.value === 'dark')
    document.documentElement.dataset.theme = mode.value
    const meta = document.querySelector('meta[name="theme-color"]')
    if (meta) meta.setAttribute('content', resolved.value === 'dark' ? '#0b0f19' : '#2563eb')
  }

  /** set changes the preference and persists it. */
  function set(value: ThemeMode): void {
    mode.value = value
    localStorage.setItem(THEME_STORAGE_KEY, value)
    apply()
  }

  /** applyDefault is used for first time visitors (Admin > Settings default). */
  function applyDefault(value: ThemeMode, force = false): void {
    if (force || !localStorage.getItem(THEME_STORAGE_KEY)) {
      mode.value = value
      apply()
    }
  }

  apply()

  return { mode, resolved, set, apply, applyDefault }
})
