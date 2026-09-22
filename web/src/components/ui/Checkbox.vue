<script setup lang="ts">
import { computed } from 'vue'
import { Check, Minus } from 'lucide-vue-next'
import { CheckboxIndicator, CheckboxRoot } from 'reka-ui'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<{ modelValue: boolean; disabled?: boolean; label?: string }>(), {
  disabled: false,
  label: '',
})
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const classes = computed(() =>
  cn(
    'flex h-4 w-4 shrink-0 items-center justify-center rounded border border-input bg-card text-primary-foreground shadow-sm focus-visible:outline-none disabled:opacity-50',
    props.modelValue && 'border-primary bg-primary',
  ),
)
</script>

<template>
  <label class="inline-flex cursor-pointer items-center gap-2 text-sm">
    <CheckboxRoot
      :model-value="props.modelValue"
      :disabled="props.disabled"
      :class="classes"
      @update:model-value="emit('update:modelValue', $event === true)"
    >
      <CheckboxIndicator class="flex items-center justify-center">
        <Check class="h-3 w-3" aria-hidden="true" />
        <Minus v-if="false" class="hidden h-3 w-3" aria-hidden="true" />
      </CheckboxIndicator>
    </CheckboxRoot>
    <span v-if="label || $slots.default" class="select-none">
      <slot>{{ label }}</slot>
    </span>
  </label>
</template>
