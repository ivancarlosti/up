<script setup lang="ts">
import { cn } from '@/lib/utils'

/**
 * The body padding is opt-out through `padded`, and it must default to true
 * explicitly: Vue casts an absent Boolean prop to false, so before this default
 * the card body had no padding at all and every table/list hugged the border.
 */
withDefaults(defineProps<{ title?: string; description?: string; padded?: boolean }>(), { padded: true })
</script>

<template>
  <section class="overflow-hidden rounded-xl border border-border bg-card text-card-foreground shadow-sm">
    <header v-if="title || $slots.header || $slots.actions" class="flex items-start justify-between gap-4 border-b border-border px-5 py-4">
      <div class="min-w-0">
        <slot name="header">
          <h2 class="truncate text-sm font-semibold tracking-tight">{{ title }}</h2>
          <p v-if="description" class="mt-1 text-xs text-muted-foreground">{{ description }}</p>
        </slot>
      </div>
      <div v-if="$slots.actions" class="flex shrink-0 items-center gap-2">
        <slot name="actions" />
      </div>
    </header>

    <div :class="cn(padded === false ? '' : 'p-5')">
      <slot />
    </div>

    <footer v-if="$slots.footer" class="flex items-center justify-end gap-2 border-t border-border px-5 py-4">
      <slot name="footer" />
    </footer>
  </section>
</template>
