<script setup lang="ts">
import { computed } from 'vue'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<{ for?: string; required?: boolean; help?: string }>(), {
  for: undefined,
  required: false,
  help: '',
})
const classes = computed(() => cn('text-xs font-medium text-muted-foreground'))
</script>

<template>
  <label :for="props.for" :class="classes">
    <slot />
    <span v-if="required" class="text-destructive" aria-hidden="true"> *</span>
    <span v-if="help" class="mt-0.5 block text-[11px] font-normal opacity-80">{{ help }}</span>
  </label>
</template>
