<script setup lang="ts">
import { computed, useAttrs } from 'vue'
import { cn } from '@/lib/utils'

defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{ modelValue?: string | number; invalid?: boolean }>(), {
  modelValue: '',
  invalid: false,
})
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const attrs = useAttrs()

const classes = computed(() =>
  cn(
    'flex h-9 w-full rounded-md border bg-card px-3 py-1 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none disabled:cursor-not-allowed disabled:opacity-50',
    props.invalid ? 'border-destructive' : 'border-input',
    attrs.class as string | undefined,
  ),
)
</script>

<template>
  <input
    v-bind="{ ...attrs, class: undefined }"
    :value="modelValue"
    :class="classes"
    @input="emit('update:modelValue', ($event.target as HTMLInputElement).value)"
  />
</template>
