/// <reference types="vite/client" />

declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent<Record<string, unknown>, Record<string, unknown>, unknown>
  export default component
}

/**
 * The subset of the Google reCAPTCHA v3 API the Recaptcha.vue component uses.
 * Loading the script is the component's job; the global only appears after it.
 *
 * v3 has no widget: `execute` mints a token for one action with a score the
 * server evaluates. There is deliberately no `render`/`reset` here, because
 * loading `api.js` without `?render=<sitekey>` makes `execute` fail.
 */
interface UpRecaptcha {
  ready(callback: () => void): void
  execute(siteKey: string, options: { action: string }): Promise<string>
}

interface Window {
  grecaptcha?: UpRecaptcha
}
