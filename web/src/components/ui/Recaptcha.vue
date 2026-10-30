<script setup lang="ts">
import { onMounted } from 'vue'

/**
 * Recaptcha mints Google reCAPTCHA v3 tokens for one form.
 *
 * v3 has no widget and no user interaction: the form calls `execute()`, the
 * component asks grecaptcha for a token bound to `action` and hands it back, and
 * the server decides from the score whether the request continues. The script is
 * loaded once and its badge is left where Google injects it (bottom right).
 *
 * Loading the API *with* `?render=<sitekey>` is mandatory: without it Google
 * answers `Invalid key type` for a v3 pair, which is exactly what the old
 * explicit v2 widget did. The key is part of the URL, so rotating it in
 * Admin > Settings reloads the script instead of reusing the old one.
 */
const props = defineProps<{ sitekey: string; action: string }>()

const scriptID = 'up-recaptcha-script'
let loaded: { sitekey: string; promise: Promise<void> } | null = null

/** load injects the v3 API for `sitekey`, reusing whatever is already there. */
function load(sitekey: string): Promise<void> {
  if (!sitekey) return Promise.reject(new Error('reCAPTCHA site key is missing'))
  if (loaded?.sitekey === sitekey) return loaded.promise
  const src = `https://www.google.com/recaptcha/api.js?render=${encodeURIComponent(sitekey)}`
  const promise = new Promise<void>((resolve, reject) => {
    const failed = (): void => reject(new Error('reCAPTCHA could not load'))
    const settle = (): void => {
      if (!window.grecaptcha) {
        reject(new Error('reCAPTCHA did not initialize'))
        return
      }
      window.grecaptcha.ready(() => resolve())
    }
    const existing = document.getElementById(scriptID) as HTMLScriptElement | null
    if (existing?.src === src) {
      // The API is already loading (or loaded) for this key: share the wait,
      // which is what keeps two forms on one page to a single script tag.
      if (window.grecaptcha) settle()
      else existing.addEventListener('load', settle, { once: true })
      existing.addEventListener('error', failed, { once: true })
      return
    }
    // A tag for another key (rotated in Admin > Settings) stays bound to that
    // key: drop it and load the API again for the current one.
    existing?.remove()
    const script = document.createElement('script')
    script.id = scriptID
    script.src = src
    script.async = true
    script.defer = true
    script.addEventListener('load', settle, { once: true })
    script.addEventListener('error', failed, { once: true })
    document.head.appendChild(script)
  })
  loaded = { sitekey, promise }
  // A failed load must not be cached: the next submit retries instead of
  // reusing a rejection for the rest of the session.
  promise.catch(() => {
    if (loaded?.promise === promise) loaded = null
  })
  return promise
}

/**
 * execute mints a fresh token for the action of this form.
 *
 * The token is valid for two minutes and can be verified once, so it must be
 * requested when the form is submitted, never on mount.
 */
async function execute(): Promise<string> {
  await load(props.sitekey)
  if (!window.grecaptcha) throw new Error('reCAPTCHA did not initialize')
  return window.grecaptcha.execute(props.sitekey, { action: props.action })
}

defineExpose({ execute })

// The script is fetched on mount so a submit does not wait for the network;
// the token itself is only minted by execute().
onMounted(() => {
  load(props.sitekey).catch(() => {
    // Swallowed here: the form reports the failure when execute() is called,
    // where it can also stop the submit and show a message.
  })
})
</script>

<template>
  <!-- v3 renders no widget: Google injects its own badge in the corner of the
       page. The component keeps an element so a caller can mount it in place. -->
  <span class="sr-only" aria-hidden="true" />
</template>
