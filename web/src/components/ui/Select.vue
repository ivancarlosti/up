<script setup lang="ts">
import { computed, useAttrs } from 'vue'
import { cn } from '@/lib/utils'

defineOptions({ inheritAttrs: false })

const props = withDefaults(
  defineProps<{
    modelValue?: string | number | boolean
    options?: { value: string | number; label: string; disabled?: boolean }[]
    placeholder?: string
  }>(),
  { modelValue: '', options: () => [], placeholder: '' },
)
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const attrs = useAttrs()

const classes = computed(() =>
  cn(
    'h-9 w-full rounded-md border border-input bg-card px-3 text-sm shadow-sm focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50',
    attrs.class as string | undefined,
  ),
)
</script>

<template>
  <select
    v-bind="{ ...attrs, class: undefined }"
    :value="modelValue"
    :class="classes"
    @change="emit('update:modelValue', ($event.target as HTMLSelectElement).value)"
  >
    <option v-if="placeholder" value="">{{ placeholder }}</option>
    <option v-for="option in options" :key="String(option.value)" :value="option.value" :disabled="option.disabled">
      {{ option.label }}
    </option>
  </select>
</template>
