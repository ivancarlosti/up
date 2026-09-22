<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, RefreshCw } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import HeartbeatBar from '@/components/monitors/HeartbeatBar.vue'
import StatusBadge from '@/components/monitors/StatusBadge.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime, formatLatency, formatUptime, statusColor } from '@/lib/format'
import { useToastStore } from '@/stores/toast'
import type { Heartbeat, Monitor, UptimeStats } from '@/lib/types'

const route = useRoute()
const router = useRouter()
const toasts = useToastStore()
const { t, locale } = useI18n()

const monitor = ref<Monitor | null>(null)
const stats = ref<UptimeStats | null>(null)
const heartbeats = ref<Heartbeat[]>([])
const hours = ref(24)
const loading = ref(false)

const monitorId = computed(() => Number(route.params.id))

/** Latency sparkline: plain divs, no charting dependency. */
const latencyBars = computed(() => {
  const items = [...heartbeats.value].slice(0, 60).reverse()
  const max = Math.max(1, ...items.map((item) => item.latency_ms))
  return items.map((item) => ({ ...item, height: Math.max(4, Math.round((item.latency_ms / max) * 100)) }))
})

async function load(): Promise<void> {
  loading.value = true
  try {
    const [detail, statsResponse, list] = await Promise.all([
      api.monitor(monitorId.value),
      api.monitorStats(monitorId.value, { hours: hours.value, limit: 80 }),
      api.monitorHeartbeats(monitorId.value, { hours: hours.value, limit: 100 }),
    ])
    monitor.value = detail
    stats.value = statsResponse.stats
    heartbeats.value = list
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    loading.value = false
  }
}

async function checkNow(): Promise<void> {
  if (!monitor.value) return
  try {
    await api.checkMonitor(monitor.value.id)
    toasts.info(t('dashboard.checkNow'), monitor.value.name)
    setTimeout(load, 1500)
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

function target(): string {
  const config = monitor.value?.config
  if (!config) return ''
  switch (monitor.value?.type) {
    case 'tcp':
      return `${config.host}:${config.port}`
    case 'dns':
      return `${config.record_type} ${config.hostname} @${config.resolver_server}`
    default:
      return config.url ?? ''
  }
}

onMounted(load)
watch(hours, load)
</script>

<template>
  <div v-if="monitor" class="flex flex-col gap-5">
    <header class="flex flex-wrap items-center gap-3">
      <Button variant="ghost" size="icon" @click="router.push({ name: 'dashboard' })">
        <ArrowLeft class="h-4 w-4" aria-hidden="true" />
      </Button>
      <div class="min-w-0">
        <h1 class="truncate text-lg font-semibold">{{ monitor.name }}</h1>
        <p class="truncate text-xs text-muted-foreground">{{ target() }}</p>
      </div>
      <StatusBadge :status="monitor.status" pulse class="ml-2" />
      <div class="ml-auto flex items-center gap-2">
        <Button variant="outline" size="sm" :loading="loading" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button variant="outline" size="sm" @click="checkNow">{{ t('dashboard.checkNow') }}</Button>
      </div>
    </header>

    <div class="grid gap-4 lg:grid-cols-4">
      <Card :title="t('common.uptime')">
        <p class="text-2xl font-semibold">{{ formatUptime(monitor.uptime_24h) }}</p>
        <p class="text-[11px] text-muted-foreground">{{ t('monitorDetail.uptime24h') }}</p>
      </Card>
      <Card :title="t('monitorDetail.responseTime')">
        <p class="text-2xl font-semibold">{{ formatLatency(monitor.last_latency_ms) }}</p>
        <p class="text-[11px] text-muted-foreground">{{ formatDateTime(monitor.last_check_at, locale) }}</p>
      </Card>
      <Card :title="t('monitorDetail.avg')">
        <p class="text-2xl font-semibold">{{ formatLatency(stats?.avg_ms ?? 0) }}</p>
        <p class="text-[11px] text-muted-foreground">
          {{ t('monitorDetail.min') }} {{ formatLatency(stats?.min_ms ?? 0) }} ·
          {{ t('monitorDetail.max') }} {{ formatLatency(stats?.max_ms ?? 0) }}
        </p>
      </Card>
      <Card :title="t('monitorDetail.p95')">
        <p class="text-2xl font-semibold">{{ formatLatency(stats?.p95_ms ?? 0) }}</p>
        <p class="text-[11px] text-muted-foreground">{{ t('common.hours') }}: {{ hours }}</p>
      </Card>
    </div>

    <Card>
      <HeartbeatBar :heartbeats="monitor.heartbeats" :size="40" />
      <div class="mt-3 flex h-24 items-end gap-0.5">
        <span
          v-for="bar in latencyBars"
          :key="bar.id"
          class="flex-1 rounded-t"
          :class="statusColor(bar.status)"
          :style="{ height: `${bar.height}%` }"
          :title="`${bar.latency_ms} ms`"
        />
      </div>
    </Card>

    <div class="grid gap-4 lg:grid-cols-2">
      <Card :title="t('monitorDetail.perNode')">
        <table class="w-full text-xs">
          <thead class="text-left text-muted-foreground">
            <tr>
              <th class="pb-2">{{ t('monitorDetail.node') }}</th>
              <th class="pb-2">{{ t('common.status') }}</th>
              <th class="pb-2">{{ t('common.latency') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="vote in monitor.votes ?? []" :key="vote.node_id" class="border-t border-border">
              <td class="py-1.5">{{ vote.node_name }}</td>
              <td class="py-1.5"><StatusBadge :status="vote.status" /></td>
              <td class="py-1.5">{{ formatLatency(vote.latency_ms) }}</td>
            </tr>
            <tr v-if="!(monitor.votes ?? []).length">
              <td class="py-2 text-muted-foreground" colspan="3">{{ t('monitorDetail.noEvents') }}</td>
            </tr>
          </tbody>
        </table>
      </Card>

      <Card :title="t('monitorDetail.events')">
        <ul class="flex flex-col gap-1.5 text-xs">
          <li
            v-for="heartbeat in heartbeats.slice(0, 12)"
            :key="heartbeat.id"
            class="flex items-center justify-between gap-2 border-b border-border pb-1.5 last:border-0"
          >
            <span class="flex items-center gap-2">
              <span class="h-2 w-2 rounded-full" :class="statusColor(heartbeat.status)" />
              <span>{{ formatDateTime(heartbeat.created_at, locale) }}</span>
            </span>
            <span class="flex items-center gap-3 text-muted-foreground">
              <span class="max-w-[16rem] truncate">{{ heartbeat.message }}</span>
              <span>{{ formatLatency(heartbeat.latency_ms) }}</span>
            </span>
          </li>
          <li v-if="!heartbeats.length" class="text-muted-foreground">{{ t('monitorDetail.noEvents') }}</li>
        </ul>
      </Card>
    </div>
  </div>
</template>

