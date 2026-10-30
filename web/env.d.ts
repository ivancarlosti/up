/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, unknown>
  export default component
}

/**
 * The subset of the Google reCAPTCHA API the Recaptcha.vue component uses.
 * Loading the script is the component's job; the global only appears after it.
 */
interface UpRecaptcha {
  ready(callback: () => void): void
  render(
    element: HTMLElement,
    options: {
      sitekey: string
      theme?: 'light' | 'dark'
      size?: 'normal' | 'compact'
      callback?: (token: string) => void
      'expired-callback'?: () => void
      'error-callback'?: () => void
    },
  ): number
  reset(widgetId?: number): void
}

interface Window {
  grecaptcha?: UpRecaptcha
}
