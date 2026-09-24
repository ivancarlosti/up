<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, RefreshCw } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import StatCard from '@/components/ui/StatCard.vue'
import HeartbeatBar from '@/components/monitors/HeartbeatBar.vue'
import StatusBadge from '@/components/monitors/StatusBadge.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime, formatLatency, formatUptime, statusColor } from '@/lib/format'
import { useToastStore } from '@/stores/toast'
import type { CertificateInfo, DomainInfo, Heartbeat, Monitor, UptimeStats } from '@/lib/types'

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

/**
 * Latency sparkline: plain divs, no charting dependency. The scale is capped at
 * the p90 of the window so a single timeout does not flatten every other bar
 * (the tooltip still shows the real value).
 */
const latencyBars = computed(() => {
  const items = [...heartbeats.value].slice(0, 60).reverse()
  if (items.length === 0) return []
  const sorted = items.map((item) => item.latency_ms).sort((a, b) => a - b)
  const p90 = sorted[Math.min(sorted.length - 1, Math.floor(sorted.length * 0.9))] ?? 1
  const max = Math.max(1, p90)
  return items.map((item) => ({
    ...item,
    over: item.latency_ms > max,
    height: Math.max(6, Math.min(100, Math.round((item.latency_ms / max) * 100))),
  }))
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
    case 'ssl':
      return `${config.host}:${config.port ?? 443}`
    default:
      return config.url ?? ''
  }
}

/** certificateVariant colours the validity badge by urgency. */
function certificateVariant(daysLeft: number): 'success' | 'warning' | 'danger' | 'secondary' {
  if (daysLeft <= 0) return 'danger'
  if (daysLeft <= 7) return 'danger'
  if (daysLeft <= 30) return 'warning'
  return 'success'
}

/** certificateTitle is the tooltip of the badge (issuer + exact expiry). */
function certificateTitle(certificate: CertificateInfo): string {
  const parts = [certificate.issuer, certificate.subject].filter(Boolean)
  return `${parts.join(' — ')} (${formatDateTime(certificate.not_after, locale.value)})`
}

/** domainTitle is the tooltip of the domain badge (registrar + source). */
function domainTitle(domain: DomainInfo): string {
  const parts = [domain.registrar, domain.domain].filter(Boolean)
  return `${parts.join(' — ')} (${formatDateTime(domain.expires_at, locale.value)})`
}

/** domainVariant colours the badge, using the manual/unavailable cases too. */
function domainVariant(domain: DomainInfo): 'success' | 'warning' | 'danger' | 'secondary' {
  if (domain.status !== 'ok') return 'secondary'
  return certificateVariant(domain.days_left)
}

onMounted(load)
watch(hours, load)
</script>

<template>
  <div v-if="monitor" class="flex flex-col gap-6">
    <header class="flex flex-wrap items-center gap-4">
      <Button variant="ghost" size="icon" @click="router.push({ name: 'dashboard' })">
        <ArrowLeft class="h-4 w-4" aria-hidden="true" />
      </Button>
      <div class="min-w-0">
        <h1 class="truncate text-lg font-semibold">{{ monitor.name }}</h1>
        <p class="truncate text-xs text-muted-foreground">{{ target() }}</p>
      </div>
      <StatusBadge :status="monitor.status" pulse class="ml-2" />
      <Badge
        v-if="monitor.certificate"
        :variant="certificateVariant(monitor.certificate.days_left)"
        :title="certificateTitle(monitor.certificate)"
        class="ml-1"
      >
        {{ t('certificate.daysLeft', { days: monitor.certificate.days_left }) }}
      </Badge>
      <Badge
        v-if="monitor.domain"
        :variant="domainVariant(monitor.domain)"
        :title="domainTitle(monitor.domain)"
        class="ml-1"
      >
        <template v-if="monitor.domain.status === 'ok'">
          {{ t('domain.daysLeft', { days: monitor.domain.days_left }) }}
        </template>
        <template v-else-if="monitor.domain.status === 'not_found'">{{ t('domain.notFound') }}</template>
        <template v-else-if="monitor.domain.status === 'unsupported'">{{ t('domain.unsupported') }}</template>
        <template v-else>{{ t('domain.unavailable') }}</template>
      </Badge>
      <div class="ml-auto flex items-center gap-2">
        <Button variant="outline" size="sm" :loading="loading" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button variant="outline" size="sm" @click="checkNow">{{ t('dashboard.checkNow') }}</Button>
      </div>
    </header>

    <div class="grid gap-5 sm:grid-cols-2 xl:grid-cols-4">
      <StatCard
        :label="t('common.uptime')"
        :value="formatUptime(monitor.uptime_24h)"
        :caption="t('monitorDetail.uptime24h')"
      />
      <StatCard
        :label="t('monitorDetail.responseTime')"
        :value="formatLatency(monitor.last_latency_ms)"
        :caption="formatDateTime(monitor.last_check_at, locale)"
      />
      <StatCard
        :label="t('monitorDetail.avg')"
        :value="formatLatency(stats?.avg_ms ?? 0)"
        :caption="`${t('monitorDetail.min')} ${formatLatency(stats?.min_ms ?? 0)} · ${t('monitorDetail.max')} ${formatLatency(stats?.max_ms ?? 0)}`"
      />
      <StatCard
        :label="t('monitorDetail.p95')"
        :value="formatLatency(stats?.p95_ms ?? 0)"
        :caption="`${t('common.hours')}: ${hours}`"
      />
    </div>

    <Card>
      <div class="flex flex-wrap items-center justify-between gap-3">
        <p class="text-xs font-medium text-muted-foreground">{{ t('monitorDetail.heartbeats') }}</p>
        <p class="text-[11px] text-muted-foreground">{{ t('monitorDetail.legend') }}</p>
      </div>
      <div class="mt-4">
        <HeartbeatBar :heartbeats="monitor.heartbeats" :size="40" />
      </div>

      <div class="mt-6 border-t border-border pt-5">
        <p class="text-xs font-medium text-muted-foreground">{{ t('monitorDetail.latency') }}</p>
        <div class="mt-3 flex h-28 items-end gap-1">
          <span
            v-for="bar in latencyBars"
            :key="bar.id"
            class="min-h-[6px] flex-1 rounded-t"
            :class="[statusColor(bar.status), bar.over && 'opacity-60']"
            :style="{ height: `${bar.height}%` }"
            :title="`${bar.latency_ms} ms`"
          />
        </div>
      </div>
    </Card>

    <div class="grid gap-5 lg:grid-cols-2">
      <Card :title="t('monitorDetail.perNode')">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('monitorDetail.node') }}</th>
              <th>{{ t('common.status') }}</th>
              <th>{{ t('common.latency') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="vote in monitor.votes ?? []" :key="vote.node_id">
              <td>{{ vote.node_name }}</td>
              <td><StatusBadge :status="vote.status" /></td>
              <td>{{ formatLatency(vote.latency_ms) }}</td>
            </tr>
            <tr v-if="!(monitor.votes ?? []).length">
              <td class="py-2 text-muted-foreground" colspan="3">{{ t('monitorDetail.noEvents') }}</td>
            </tr>
          </tbody>
        </table>
      </Card>

      <Card :title="t('monitorDetail.events')">
        <ul class="flex flex-col">
          <li
            v-for="heartbeat in heartbeats.slice(0, 12)"
            :key="heartbeat.id"
            class="flex items-center justify-between gap-3 border-b border-border/60 py-2.5 text-xs last:border-0"
          >
            <span class="flex min-w-0 items-center gap-2.5">
              <span class="h-2 w-2 shrink-0 rounded-full" :class="statusColor(heartbeat.status)" />
              <span class="truncate">{{ formatDateTime(heartbeat.created_at, locale) }}</span>
            </span>
            <span class="flex shrink-0 items-center gap-3 text-muted-foreground">
              <span class="max-w-[14rem] truncate sm:max-w-[16rem]">{{ heartbeat.message }}</span>
              <span class="tabular-nums">{{ formatLatency(heartbeat.latency_ms) }}</span>
            </span>
          </li>
          <li v-if="!heartbeats.length" class="py-2 text-muted-foreground">{{ t('monitorDetail.noEvents') }}</li>
        </ul>
      </Card>
    </div>
  </div>
</template>

