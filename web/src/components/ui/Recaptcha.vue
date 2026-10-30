<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'

/**
 * Recaptcha renders Google's explicit reCAPTCHA widget.
 *
 * The login page used to carry the `.g-recaptcha` markup without ever loading
 * the Google script, so the widget never appeared and the hidden token field was
 * always empty. This component loads the script once per page, renders the
 * widget into its own element and exposes the token through v-model, which fixes
 * login and lets the public status page use the same widget.
 */
const props = withDefaults(defineProps<{ sitekey: string; theme?: 'light' | 'dark' }>(), {
  theme: 'light',
})
const emit = defineEmits<{
  'update:modelValue': [value: string]
  error: []
}>()

const container = ref<HTMLElement | null>(null)
const widgetId = ref<number | null>(null)
const failed = ref(false)

let scriptPromise: Promise<void> | null = null

/** loadScript loads the reCAPTCHA API once, even with several widgets. */
function loadScript(): Promise<void> {
  if (window.grecaptcha) return Promise.resolve()
  if (scriptPromise) return scriptPromise
  scriptPromise = new Promise<void>((resolve, reject) => {
    const existing = document.getElementById('up-recaptcha-script') as HTMLScriptElement | null
    const ready = (): void => {
      if (!window.grecaptcha) {
        reject(new Error('reCAPTCHA did not initialize'))
        return
      }
      window.grecaptcha.ready(() => resolve())
    }
    if (existing) {
      if (window.grecaptcha) ready()
      else existing.addEventListener('load', ready, { once: true })
      return
    }
    const script = document.createElement('script')
    script.id = 'up-recaptcha-script'
    script.src = 'https://www.google.com/recaptcha/api.js?render=explicit'
    script.async = true
    script.defer = true
    script.addEventListener('load', ready, { once: true })
    script.addEventListener('error', () => reject(new Error('reCAPTCHA could not load')), { once: true })
    document.head.appendChild(script)
  })
  return scriptPromise
}

async function render(): Promise<void> {
  if (!props.sitekey || !container.value) return
  try {
    await loadScript()
    if (!window.grecaptcha || !container.value) return
    widgetId.value = window.grecaptcha.render(container.value, {
      sitekey: props.sitekey,
      theme: props.theme,
      callback: (token: string) => emit('update:modelValue', token),
      'expired-callback': () => emit('update:modelValue', ''),
      'error-callback': () => {
        emit('update:modelValue', '')
        failed.value = true
        emit('error')
      },
    })
  } catch {
    failed.value = true
    emit('error')
  }
}

/** reset clears the widget and the token, e.g. after a rejected submit. */
function reset(): void {
  emit('update:modelValue', '')
  if (widgetId.value !== null && window.grecaptcha) {
    try {
      window.grecaptcha.reset(widgetId.value)
    } catch {
      // The widget was already removed with the component.
    }
  }
}

defineExpose({ reset })

onMounted(render)
onBeforeUnmount(() => {
  if (widgetId.value !== null && window.grecaptcha) {
    try {
      window.grecaptcha.reset(widgetId.value)
    } catch {
      // Nothing to reset: the element is gone with the component.
    }
  }
})
watch(
  () => props.sitekey,
  async () => {
    failed.value = false
    emit('update:modelValue', '')
    await render()
  },
)
</script>

<template>
  <div class="flex flex-col gap-1">
    <div ref="container" />
    <p v-if="failed" class="text-[11px] text-destructive">{{ $t('login.captchaUnavailable') }}</p>
  </div>
</template>
