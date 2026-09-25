<script setup lang="ts">
import { computed } from 'vue'
import { SwitchRoot, SwitchThumb } from 'reka-ui'
import { cn } from '@/lib/utils'

const props = withDefaults(defineProps<{ modelValue: boolean; disabled?: boolean; label?: string }>(), {
  disabled: false,
  label: '',
})
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()

const track = computed(() =>
  cn(
    'relative inline-flex h-5 w-9 shrink-0 cursor-pointer items-center rounded-full border-2 border-transparent transition-colors focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50',
    props.modelValue ? 'bg-primary' : 'bg-muted',
  ),
)
</script>

<template>
  <label class="inline-flex cursor-pointer items-center gap-2 text-sm">
    <SwitchRoot
      :model-value="props.modelValue"
      :disabled="props.disabled"
      :class="track"
      @update:model-value="emit('update:modelValue', $event === true)"
    >
      <SwitchThumb
        class="pointer-events-none block h-4 w-4 rounded-full bg-card shadow transition-transform data-[state=checked]:translate-x-4 rtl:data-[state=checked]:-translate-x-4"
      />
    </SwitchRoot>
    <span v-if="label || $slots.default" class="select-none">
      <slot>{{ label }}</slot>
    </span>
  </label>
</template>
