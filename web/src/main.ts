import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from '@/App.vue'
import router from '@/router'
import { i18n } from '@/i18n'
import '@/style.css'

/**
 * Entry point of the Up frontend.
 *
 * The app is a single page application served by the Go binary (web/embed.go);
 * during development Vite proxies /api and the WebSocket to the backend.
 */
const app = createApp(App)

app.use(createPinia())
app.use(i18n)
app.use(router)

/**
 * Render a startup failure inside #app.
 *
 * A failed boot used to leave <div id="app"></div> empty - a blank page with the
 * real cause buried in the console. The fallback only takes over while nothing
 * has been rendered, so an exception raised later (a toast, one broken card)
 * never replaces an application that is already on screen.
 */
function renderBootFailure(error: unknown): void {
  const container = document.getElementById('app')
  if (!container || container.childElementCount > 0) return

  const message = error instanceof Error ? `${error.name}: ${error.message}` : String(error)

  const box = document.createElement('div')
  box.setAttribute('role', 'alert')
  box.style.cssText =
    'max-width:40rem;margin:12vh auto;padding:1.5rem;border:1px solid #dc2626;border-radius:.5rem;' +
    'font-family:ui-sans-serif,system-ui,sans-serif;line-height:1.6'

  const title = document.createElement('h1')
  title.textContent = 'Up could not start'
  title.style.cssText = 'margin:0 0 .5rem;font-size:1.125rem;color:#dc2626'

  const detail = document.createElement('p')
  detail.textContent = message
  detail.style.cssText = 'margin:0 0 .75rem;word-break:break-word'

  const hint = document.createElement('p')
  hint.textContent =
    'Reload the page. If it keeps failing, check the console of the browser and the logs of the server.'
  hint.style.cssText = 'margin:0;font-size:.875rem;opacity:.75'

  box.append(title, detail, hint)
  container.append(box)
}

app.config.errorHandler = (error) => {
  // Keep the console informative: the UI shows the localized message through
  // the toast store, but the raw error is useful while debugging.
  console.error('[up] unexpected error', error)
  renderBootFailure(error)
}

// Vue Router reports the errors raised by a navigation guard here; without this
// hook they would only show up as an unhandled rejection in the console.
router.onError((error) => {
  console.error('[up] navigation failed', error)
  renderBootFailure(error)
})

window.addEventListener('unhandledrejection', (event) => {
  console.error('[up] unhandled rejection', event.reason)
  renderBootFailure(event.reason)
})

try {
  app.mount('#app')
} catch (error) {
  console.error('[up] failed to start', error)
  renderBootFailure(error)
}
