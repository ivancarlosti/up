<script setup lang="ts">
import { computed } from 'vue'
import { Languages } from 'lucide-vue-next'
import { useAppStore } from '@/stores/app'
import { i18n, localeLabel, SUPPORTED_LOCALES } from '@/i18n'

const app = useAppStore()

const current = computed(() => i18n.global.locale.value)
const options = computed(() =>
  SUPPORTED_LOCALES.map((locale) => ({ code: locale, label: localeLabel(locale) })),
)

function change(event: Event): void {
  app.setLocale((event.target as HTMLSelectElement).value)
}
</script>

<template>
  <label class="relative inline-flex items-center gap-1 rounded-md border border-border bg-card px-2 py-1">
    <Languages class="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
    <span class="sr-only">{{ $t('app.instance') }}</span>
    <select
      :value="current"
      class="cursor-pointer bg-transparent text-xs font-medium focus-visible:outline-none"
      @change="change"
    >
      <option v-for="option in options" :key="option.code" :value="option.code">{{ option.label }}</option>
    </select>
  </label>
</template>
