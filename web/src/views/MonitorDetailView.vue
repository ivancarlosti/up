<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, Pencil, RefreshCw } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Select from '@/components/ui/Select.vue'
import StatCard from '@/components/ui/StatCard.vue'
import HeartbeatBar from '@/components/monitors/HeartbeatBar.vue'
import MonitorForm from '@/components/monitors/MonitorForm.vue'
import StatusBadge from '@/components/monitors/StatusBadge.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { certificateTitle, domainTitle, expiryStateVariant, expiryVariant } from '@/lib/expiry'
import { formatDateTime, formatLatency, formatUptime, formatUptimeWindow, statusColor } from '@/lib/format'
import { uptimeWindowOptions } from '@/lib/uptime-window'
import { useAppStore } from '@/stores/app'
import { useToastStore } from '@/stores/toast'
import type {
  Heartbeat,
  Monitor,
  MonitorGroup,
  MonitorPayload,
  MonitorTemplate,
  Notification,
  UptimeStats,
} from '@/lib/types'

const route = useRoute()
const router = useRouter()
const toasts = useToastStore()
const app = useAppStore()
const { t, locale } = useI18n()

const monitor = ref<Monitor | null>(null)
const stats = ref<UptimeStats | null>(null)
const heartbeats = ref<Heartbeat[]>([])
/**
 * The period the statistics and the bars cover. It starts at the global uptime
 * window (Admin > Settings) and the operator can pick any other offered period.
 */
const hours = ref(app.settings?.uptime_window_hours ?? 24)
const loading = ref(false)

/** windowValue adapts the numeric window to the string Select. */
const windowValue = computed({
  get: () => String(hours.value),
  set: (value: string) => {
    hours.value = Number(value)
  },
})

const windowOptions = computed(() => uptimeWindowOptions())

// The edit dialog is the same form as the monitors page: it needs the channel,
// group and template lists for its pickers.
const formOpen = ref(false)
const saving = ref(false)
const notifications = ref<Notification[]>([])
const groups = ref<MonitorGroup[]>([])
const templates = ref<MonitorTemplate[]>([])

const monitorId = computed(() => Number(route.params.id))

/**
 * Latency sparkline: plain divs, no charting dependency.
 *
 * The bars are scaled to the p90 of the plotted samples so a single timeout does
 * not flatten every other bar, which means the top of the chart is NOT the
 * maximum. Everything the reader needs to interpret it is therefore written next
 * to it: the value of the scale, a dashed line at the average, and a min/avg/max
 * line computed from the very samples that are drawn. A bar taller than the
 * scale is dimmed on purpose - its real value is in its tooltip.
 */
const latencySamples = computed(() => [...heartbeats.value].slice(0, 60).reverse())

/** latencyScale is the p90 of the samples, floored at 1 ms so a ratio is always defined. */
const latencyScale = computed(() => {
  const values = latencySamples.value.map((item) => item.latency_ms).sort((a, b) => a - b)
  if (values.length === 0) return 1
  return Math.max(1, values[Math.min(values.length - 1, Math.floor(values.length * 0.9))] ?? 1)
})

const latencyBars = computed(() =>
  latencySamples.value.map((item) => ({
    ...item,
    over: item.latency_ms > latencyScale.value,
    height: Math.max(6, Math.min(100, Math.round((item.latency_ms / latencyScale.value) * 100))),
  })),
)

/**
 * latencyStats describes the plotted samples themselves, not the whole window:
 * the numbers under the chart have to be the ones the bars are drawn from, or
 * the chart and its own legend would contradict each other.
 */
const latencyStats = computed(() => {
  const values = latencySamples.value.map((item) => item.latency_ms)
  if (values.length === 0) {
    return { count: 0, min: 0, avg: 0, max: 0, avgHeight: 0, overCount: 0, over: false }
  }
  const avg = Math.round(values.reduce((sum, value) => sum + value, 0) / values.length)
  const overCount = values.filter((value) => value > latencyScale.value).length
  return {
    count: values.length,
    min: Math.min(...values),
    avg,
    max: Math.max(...values),
    // The dashed average line shares the scale of the bars, so the two are
    // directly comparable.
    avgHeight: Math.max(0, Math.min(100, Math.round((avg / latencyScale.value) * 100))),
    overCount,
    over: overCount > 0,
  }
})

/** latencyTitle is the per bar tooltip: status, formatted latency and timestamp. */
function latencyTitle(bar: Heartbeat): string {
  return `${t(`status.${bar.status}`)} · ${formatLatency(bar.latency_ms)} · ${formatDateTime(bar.created_at, locale.value)}`
}

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

/** targetHref turns the target into a link when it is an URL. */
const targetHref = computed(() => {
  const type = monitor.value?.type
  if (type !== 'http' && type !== 'keyword') return ''
  const url = monitor.value?.config?.url ?? ''
  return /^https?:\/\//i.test(url) ? url : ''
})

/** loadFormOptions fills the lists the edit dialog needs. */
async function loadFormOptions(): Promise<void> {
  try {
    const [channelList, groupList, templateList] = await Promise.all([
      api.notifications(),
      api.monitorGroups(),
      api.monitorTemplates(),
    ])
    notifications.value = channelList
    groups.value = groupList
    templates.value = templateList
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

function openEdit(): void {
  formOpen.value = true
}

async function submit(payload: MonitorPayload): Promise<void> {
  if (!monitor.value) return
  saving.value = true
  try {
    await api.updateMonitor(monitor.value.id, payload)
    formOpen.value = false
    toasts.success(t('common.saved'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

onMounted(load)
onMounted(loadFormOptions)
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
        <a
          v-if="targetHref"
          class="block truncate text-xs text-muted-foreground hover:text-foreground hover:underline"
          :href="targetHref"
          target="_blank"
          rel="noopener noreferrer"
        >
          {{ target() }}
        </a>
        <p v-else class="truncate text-xs text-muted-foreground">{{ target() }}</p>
      </div>
      <Badge v-if="monitor.template_name" variant="outline" :title="t('monitor.templateSection')">
        {{ monitor.template_name }}
      </Badge>
      <StatusBadge :status="monitor.status" pulse class="ms-2" />
      <Badge
        v-if="monitor.certificate"
        :variant="expiryVariant(monitor.certificate.days_left)"
        :title="certificateTitle(monitor.certificate, locale)"
        class="ms-1"
      >
        {{ t('certificate.daysLeft', { days: monitor.certificate.days_left }) }}
      </Badge>
      <Badge
        v-if="monitor.domain"
        :variant="expiryStateVariant(monitor.domain.status, monitor.domain.days_left)"
        :title="domainTitle(monitor.domain, locale)"
        class="ms-1"
      >
        <template v-if="monitor.domain.status === 'ok'">
          {{ t('domain.daysLeft', { days: monitor.domain.days_left }) }}
        </template>
        <template v-else-if="monitor.domain.status === 'not_found'">{{ t('domain.notFound') }}</template>
        <template v-else-if="monitor.domain.status === 'unsupported'">{{ t('domain.unsupported') }}</template>
        <template v-else>{{ t('domain.unavailable') }}</template>
      </Badge>
      <div class="ms-auto flex items-center gap-2">
        <Select v-model="windowValue" :options="windowOptions" class="w-24" :aria-label="t('common.uptime')" />
        <Button variant="outline" size="sm" @click="openEdit">
          <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.edit') }}
        </Button>
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
        :value="formatUptime(stats?.uptime ?? monitor.uptime)"
        :caption="formatUptimeWindow(hours)"
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
        <div class="flex flex-wrap items-center justify-between gap-2">
          <p class="text-xs font-medium text-muted-foreground">{{ t('monitorDetail.latency') }}</p>
          <p v-if="latencyStats.count" class="text-[11px] text-muted-foreground">
            {{ formatUptimeWindow(hours) }} · {{ t('monitorDetail.samples', { count: latencyStats.count }) }}
          </p>
        </div>

        <p v-if="!latencyStats.count" class="mt-3 text-xs text-muted-foreground">
          {{ t('monitorDetail.noEvents') }}
        </p>

        <template v-else>
          <div
            class="relative mt-4 h-28"
            role="img"
            :aria-label="`${t('monitorDetail.latency')}: ${t('monitorDetail.min')} ${formatLatency(latencyStats.min)}, ${t('monitorDetail.avg')} ${formatLatency(latencyStats.avg)}, ${t('monitorDetail.max')} ${formatLatency(latencyStats.max)}`"
          >
            <span
              class="absolute end-0 top-0 z-10 rounded bg-card/90 px-1 text-[10px] text-muted-foreground"
              :title="t('monitorDetail.scaleHint')"
            >
              {{ t('monitorDetail.scale', { value: formatLatency(latencyScale) }) }}
            </span>
            <div
              class="pointer-events-none absolute inset-x-0 border-t border-dashed border-muted-foreground/60"
              :style="{ bottom: `${latencyStats.avgHeight}%` }"
            >
              <span
                class="absolute start-0 -translate-y-1/2 rounded bg-card/90 px-1 text-[10px] text-muted-foreground"
              >
                {{ t('monitorDetail.avg') }} {{ formatLatency(latencyStats.avg) }}
              </span>
            </div>
            <div class="flex h-full items-end gap-1">
              <span
                v-for="bar in latencyBars"
                :key="bar.id"
                class="min-h-[6px] flex-1 rounded-t"
                :class="[statusColor(bar.status), bar.over && 'opacity-60']"
                :style="{ height: `${bar.height}%` }"
                :title="latencyTitle(bar)"
              />
            </div>
          </div>

          <div class="mt-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
            <span>
              {{ t('monitorDetail.min') }}
              <span class="tabular-nums text-foreground">{{ formatLatency(latencyStats.min) }}</span>
            </span>
            <span>
              {{ t('monitorDetail.avg') }}
              <span class="tabular-nums text-foreground">{{ formatLatency(latencyStats.avg) }}</span>
            </span>
            <span>
              {{ t('monitorDetail.max') }}
              <span class="tabular-nums text-foreground">{{ formatLatency(latencyStats.max) }}</span>
            </span>
            <span v-if="latencyStats.over" :title="t('monitorDetail.scaleHint')">
              {{ t('monitorDetail.overScale', { count: latencyStats.overCount }) }}
            </span>
            <span class="ms-auto">{{ t('monitorDetail.legend') }}</span>
          </div>
        </template>
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

    <MonitorForm
      v-model="formOpen"
      :monitor="monitor"
      :notifications="notifications"
      :groups="groups"
      :templates="templates"
      :saving="saving"
      @submit="submit"
    />
  </div>
</template>

