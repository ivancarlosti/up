<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { statusColor } from '@/lib/format'
import type { HeartbeatStatus } from '@/lib/types'

/**
 * HeartbeatSparkline is the compact, bucketed history shown in the monitors
 * table.
 *
 * The data is a snapshot computed by the server (one grouped query for the whole
 * list), never updated live: the column must not turn a dashboard load into one
 * request per monitor. A slot without a heartbeat (a paused monitor, or a gap in
 * the history) renders as the muted "unknown" colour.
 */
const props = withDefaults(defineProps<{ bars?: string[] }>(), { bars: () => [] })

const { t } = useI18n()

function colorFor(status: string): string {
  return status ? statusColor(status as HeartbeatStatus) : 'bg-status-unknown/40'
}
</script>

<template>
  <div class="heartbeat-bar w-full min-w-20" role="img" :title="t('monitor.heartbeatTitle')">
    <span v-for="(status, index) in props.bars" :key="index" :class="colorFor(status)" />
  </div>
</template>
