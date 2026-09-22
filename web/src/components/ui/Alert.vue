<script setup lang="ts">
import { computed } from 'vue'
import { AlertTriangle, CheckCircle2, Info, XCircle } from 'lucide-vue-next'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<{ variant?: 'info' | 'success' | 'warning' | 'danger' }>(), {
  variant: 'info',
})

const icons = { info: Info, success: CheckCircle2, warning: AlertTriangle, danger: XCircle }

const classes = computed(() =>
  cn('flex items-start gap-3 rounded-lg border px-3 py-2 text-xs', {
    'border-primary/30 bg-primary/10 text-foreground': props.variant === 'info',
    'border-status-up/30 bg-status-up/10 text-foreground': props.variant === 'success',
    'border-status-degraded/40 bg-status-degraded/10 text-foreground': props.variant === 'warning',
    'border-status-down/40 bg-status-down/10 text-foreground': props.variant === 'danger',
  }),
)
</script>

<template>
  <div :class="classes" role="status">
    <component :is="icons[props.variant]" class="mt-0.5 h-4 w-4 shrink-0" aria-hidden="true" />
    <div class="min-w-0 flex-1">
      <slot />
    </div>
  </div>
</template>
