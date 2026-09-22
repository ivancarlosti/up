<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { statusColor, statusTextColor } from '@/lib/format'
import type { AggregateStatus } from '@/lib/types'

const props = withDefaults(defineProps<{ status: AggregateStatus; pulse?: boolean; compact?: boolean }>(), {
  pulse: false,
  compact: false,
})

const { t } = useI18n()
const dot = computed(() => statusColor(props.status))
const text = computed(() => statusTextColor(props.status))
</script>

<template>
  <span class="inline-flex items-center gap-1.5 text-xs font-medium" :class="text">
    <span class="relative flex h-2 w-2">
      <span v-if="props.pulse && props.status === 'down'" class="absolute inline-flex h-full w-full animate-ping rounded-full opacity-75" :class="dot" />
      <span class="relative inline-flex h-2 w-2 rounded-full" :class="dot" />
    </span>
    <span v-if="!props.compact">{{ t(`status.${props.status}`) }}</span>
  </span>
</template>
