<script setup lang="ts">
import { computed } from 'vue'
import { Monitor, Moon, Sun } from 'lucide-vue-next'
import { storeToRefs } from 'pinia'
import { useThemeStore } from '@/stores/theme'
import { cn } from '@/lib/utils'
import type { ThemeMode } from '@/lib/types'

const theme = useThemeStore()
const { mode } = storeToRefs(theme)

const modes: { value: ThemeMode; icon: typeof Sun; labelKey: string }[] = [
  { value: 'light', icon: Sun, labelKey: 'settings.themeLight' },
  { value: 'dark', icon: Moon, labelKey: 'settings.themeDark' },
  { value: 'system', icon: Monitor, labelKey: 'settings.themeSystem' },
]

function classesFor(value: ThemeMode): string {
  return cn(
    'flex h-6 w-6 items-center justify-center rounded text-muted-foreground transition-colors hover:text-foreground',
    mode.value === value && 'bg-accent text-foreground',
  )
}

const currentLabel = computed(() => modes.find((item) => item.value === mode.value)?.labelKey ?? 'settings.themeSystem')
</script>

<template>
  <div class="inline-flex items-center gap-0.5 rounded-md border border-border bg-card p-0.5" role="group">
    <button
      v-for="item in modes"
      :key="item.value"
      type="button"
      :class="classesFor(item.value)"
      :title="$t(item.labelKey)"
      :aria-pressed="mode === item.value"
      @click="theme.set(item.value)"
    >
      <component :is="item.icon" class="h-3.5 w-3.5" aria-hidden="true" />
    </button>
    <span class="sr-only">{{ $t(currentLabel) }}</span>
  </div>
</template>
