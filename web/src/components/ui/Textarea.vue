<script setup lang="ts">
import { computed, useAttrs } from 'vue'
import { cn } from '@/lib/utils'

defineOptions({ inheritAttrs: false })

const props = withDefaults(defineProps<{ modelValue?: string; rows?: number; invalid?: boolean }>(), {
  modelValue: '',
  rows: 4,
  invalid: false,
})
const emit = defineEmits<{ 'update:modelValue': [value: string] }>()
const attrs = useAttrs()

const classes = computed(() =>
  cn(
    'flex w-full rounded-md border bg-card px-3 py-2 font-mono text-xs shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none',
    props.invalid ? 'border-destructive' : 'border-input',
    attrs.class as string | undefined,
  ),
)
</script>

<template>
  <textarea
    v-bind="{ ...attrs, class: undefined }"
    :rows="rows"
    :value="modelValue"
    :class="classes"
    @input="emit('update:modelValue', ($event.target as HTMLTextAreaElement).value)"
  />
</template>
