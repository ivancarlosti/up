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

app.config.errorHandler = (error) => {
  // Keep the console informative: the UI shows the localized message through
  // the toast store, but the raw error is useful while debugging.
  console.error('[up] unexpected error', error)
}

app.mount('#app')
