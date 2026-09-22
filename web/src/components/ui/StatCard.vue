<script setup lang="ts">
/**
 * StatCard is the metric tile shared by the dashboard and the monitor detail:
 * a muted label, a tabular figure and an optional caption, so numbers from
 * different screens line up instead of drifting apart.
 */
defineProps<{
  label: string
  value: string
  caption?: string
}>()
</script>

<template>
  <div class="rounded-xl border border-border bg-card px-5 py-4 text-card-foreground shadow-sm">
    <p class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ label }}</p>
    <p class="mt-2 text-2xl font-semibold leading-none tabular-nums">{{ value }}</p>
    <p v-if="caption" class="mt-2 text-xs text-muted-foreground">{{ caption }}</p>
  </div>
</template>
