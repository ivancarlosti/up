<script setup lang="ts">
import { computed } from 'vue'
import { cva, type VariantProps } from 'class-variance-authority'
import { cn } from '@/lib/utils'

const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium leading-none',
  {
    variants: {
      variant: {
        default: 'border-transparent bg-primary/15 text-primary',
        secondary: 'border-transparent bg-muted text-muted-foreground',
        outline: 'border-border text-foreground',
        success: 'border-transparent bg-status-up/15 text-status-up',
        danger: 'border-transparent bg-status-down/15 text-status-down',
        warning: 'border-transparent bg-status-degraded/20 text-status-degraded',
      },
    },
    defaultVariants: { variant: 'default' },
  },
)

const props = defineProps<{ variant?: VariantProps<typeof badgeVariants>['variant'] }>()
const classes = computed(() => cn(badgeVariants({ variant: props.variant })))
</script>

<template>
  <span :class="classes"><slot /></span>
</template>
