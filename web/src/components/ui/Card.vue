<script setup lang="ts">
import { cn } from '@/lib/utils'

defineProps<{ title?: string; description?: string; padded?: boolean }>()
</script>

<template>
  <section class="rounded-xl border border-border bg-card text-card-foreground shadow-sm">
    <header v-if="title || $slots.header || $slots.actions" class="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
      <div class="min-w-0">
        <slot name="header">
          <h2 class="truncate text-sm font-semibold">{{ title }}</h2>
          <p v-if="description" class="mt-0.5 text-xs text-muted-foreground">{{ description }}</p>
        </slot>
      </div>
      <div v-if="$slots.actions" class="flex shrink-0 items-center gap-2">
        <slot name="actions" />
      </div>
    </header>

    <div :class="cn(padded === false ? '' : 'p-4')">
      <slot />
    </div>

    <footer v-if="$slots.footer" class="flex items-center justify-end gap-2 border-t border-border px-4 py-3">
      <slot name="footer" />
    </footer>
  </section>
</template>
