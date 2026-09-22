<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { statusColor } from '@/lib/format'
import { buildBars } from '@/lib/format'
import type { HeartbeatSummary } from '@/lib/types'

const props = withDefaults(defineProps<{ heartbeats?: HeartbeatSummary[]; size?: number }>(), {
  heartbeats: () => [],
  size: 30,
})

const { t, locale } = useI18n()
const bars = computed(() => buildBars(props.heartbeats, props.size))

function titleFor(index: number): string {
  const heartbeat = (props.heartbeats ?? []).slice(-props.size)[index]
  if (!heartbeat) return t('monitorDetail.noEvents')
  const time = new Intl.DateTimeFormat(locale.value, { dateStyle: 'short', timeStyle: 'medium' }).format(
    new Date(heartbeat.created_at),
  )
  return `${t(`status.${heartbeat.status}`)} · ${heartbeat.latency_ms} ms · ${time}`
}
</script>

<template>
  <div class="heartbeat-bar w-full" role="img">
    <span
      v-for="(status, index) in bars"
      :key="index"
      :class="status ? statusColor(status) : 'bg-status-unknown/40'"
      :title="titleFor(index)"
    />
  </div>
</template>
