<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink, useRoute } from 'vue-router'
import { ArrowLeft, BellRing, HardDrive, RefreshCw } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Select from '@/components/ui/Select.vue'
import MetricChart from '@/components/systems/MetricChart.vue'
import SystemAlertsDialog from '@/components/systems/SystemAlertsDialog.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatBytes, formatDateTime, formatPercent } from '@/lib/format'
import type { ContainerPoint, SmartDisk, SystemHistory } from '@/lib/types-systems'
import { SYSTEM_CHART_WINDOWS } from '@/lib/types-systems'
import { containerRunning } from '@/stores/systems'

/**
 * SystemDetailView is one machine: its charts, its containers and its disks.
 *
 * The whole page is one payload (api.systemHistory) because the three sections are
 * always shown together: four requests would let the charts disagree with the
 * container list while they load, and a chart that is older than the table next to
 * it is worse than no chart.
 */
const { t, locale } = useI18n()
const route = useRoute()

const id = computed(() => Number(route.params.id))
const hours = ref(24)
const history = ref<SystemHistory | null>(null)
const loading = ref(false)
const error = ref<string | null>(null)
/** alertsOpen shows the alert rules/channels dialog of this machine. */
const alertsOpen = ref(false)

/** selectedContainer is the container whose history is charted below the table. */
const selectedContainer = ref<string | null>(null)
const containerSeries = ref<ContainerPoint[]>([])
const containerLoading = ref(false)
/** expandedDisk is the SMART device whose attributes are shown. */
const expandedDisk = ref<string | null>(null)

const windowOptions = computed(() =>
  SYSTEM_CHART_WINDOWS.map((value) => ({ value: String(value), label: t(`systems.window${value}`) })),
)

/** series extracts one column of the history as the values of a chart. */
function series(pick: (point: SystemHistory['metrics'][number]) => number): number[] {
  return (history.value?.metrics ?? []).map(pick)
}

const disks = computed<[string, SmartDisk][]>(() => Object.entries(history.value?.smart ?? {}))

async function load(): Promise<void> {
  if (!Number.isFinite(id.value) || id.value <= 0) return
  loading.value = true
  error.value = null
  try {
    history.value = await api.systemHistory(id.value, hours.value)
    if (selectedContainer.value) await loadContainer(selectedContainer.value)
  } catch (cause) {
    error.value = translateError(cause)
  } finally {
    loading.value = false
  }
}

/** loadContainer fetches the history of one container of this machine. */
async function loadContainer(name: string): Promise<void> {
  selectedContainer.value = name
  containerLoading.value = true
  try {
    containerSeries.value = await api.containerMetrics(id.value, name, hours.value)
  } catch {
    containerSeries.value = []
  } finally {
    containerLoading.value = false
  }
}

function toggleContainer(name: string): void {
  if (selectedContainer.value === name) {
    selectedContainer.value = null
    containerSeries.value = []
    return
  }
  void loadContainer(name)
}

function toggleDisk(device: string): void {
  expandedDisk.value = expandedDisk.value === device ? null : device
}

function statusVariant(status?: string): 'success' | 'danger' | 'warning' {
  if (status === 'up') return 'success'
  if (status === 'down') return 'danger'
  return 'warning'
}

function smartVariant(status?: string): 'success' | 'danger' | 'warning' {
  const verdict = (status ?? '').toUpperCase()
  if (verdict === 'PASSED' || verdict === 'OK' || verdict === 'GOOD') return 'success'
  if (verdict === '') return 'warning'
  return 'danger'
}

/** healthVariant colours a container health verdict (healthy / starting / unhealthy). */
function healthVariant(health?: string): 'success' | 'danger' | 'warning' {
  const verdict = (health ?? '').toLowerCase()
  if (verdict === 'healthy') return 'success'
  if (verdict === 'starting') return 'warning'
  return 'danger'
}

watch(hours, load)
onMounted(load)
onBeforeUnmount(() => {
  history.value = null
})
</script>

<template>
  <div class="space-y-5">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <RouterLink :to="{ name: 'systems' }" class="inline-flex items-center gap-1 text-xs text-muted-foreground hover:underline">
          <ArrowLeft class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('systems.backToSystems') }}
        </RouterLink>
        <h1 class="mt-1 flex items-center gap-2 text-lg font-semibold tracking-tight">
          {{ history?.system.name ?? t('common.loading') }}
          <Badge v-if="history" :variant="statusVariant(history.system.status)">{{ t(`status.${history.system.status}`) }}</Badge>
        </h1>
        <p v-if="history" class="mt-1 text-xs text-muted-foreground">
          {{ history.system.hostname || history.system.host || t('systems.noHost') }}
          <span v-if="history.system.os_name"> · {{ history.system.os_name }}</span>
          <span v-if="history.system.kernel"> · {{ history.system.kernel }}</span>
          <span v-if="history.system.cores"> · {{ t('systems.coresThreads', { cores: history.system.cores, threads: history.system.threads ?? history.system.cores }) }}</span>
          <span v-if="history.system.memory_total"> · {{ formatBytes(history.system.memory_total) }}</span>
          <span v-if="history.system.agent_version"> · agent {{ history.system.agent_version }}</span>
        </p>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" :disabled="!history" @click="alertsOpen = true">
          <BellRing class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('systems.alertsButton') }}
        </Button>
        <Select v-model="hours" :options="windowOptions" class="w-32" />
        <Button variant="outline" size="sm" :loading="loading" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
      </div>
    </header>

    <SystemAlertsDialog v-model="alertsOpen" :system="history?.system ?? null" />

    <p v-if="error" class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
      {{ error }}
    </p>
    <p v-if="history?.remote" class="rounded-md border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground">
      {{ t('systems.remoteHistory', { node: history.system.owner_node_name ?? history.system.owner_node ?? '' }) }}
    </p>
    <p
      v-if="history && history.system.status === 'down' && history.system.last_error"
      class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive"
    >
      {{ history.system.last_error }}
    </p>

    <!-- Charts ---------------------------------------------------------- -->
    <div class="grid gap-3 lg:grid-cols-2">
      <Card :title="t('systems.chartCpu')">
        <MetricChart :values="series((point) => point.cpu)" unit="percent" />
      </Card>
      <Card :title="t('systems.chartMemory')">
        <MetricChart :values="series((point) => point.mem_pct)" unit="percent" tone="text-status-degraded" />
      </Card>
      <Card :title="t('systems.chartDisk')">
        <MetricChart :values="series((point) => point.disk_pct)" unit="percent" tone="text-status-down" />
      </Card>
      <Card :title="t('systems.chartLoad')">
        <MetricChart :values="series((point) => point.load_avg)" unit="number" auto-scale />
      </Card>
      <Card :title="t('systems.chartNetworkSent')">
        <MetricChart :values="series((point) => point.net_sent)" unit="rate" />
      </Card>
      <Card :title="t('systems.chartNetworkRecv')">
        <MetricChart :values="series((point) => point.net_recv)" unit="rate" tone="text-status-degraded" />
      </Card>
      <Card :title="t('systems.chartDiskRead')">
        <MetricChart :values="series((point) => point.disk_read)" unit="rate" tone="text-status-up" />
      </Card>
      <Card :title="t('systems.chartDiskWrite')">
        <MetricChart :values="series((point) => point.disk_write)" unit="rate" tone="text-status-degraded" />
      </Card>
      <Card v-if="series((point) => point.temperature).some((value) => value > 0)" :title="t('systems.chartTemperature')">
        <MetricChart :values="series((point) => point.temperature)" unit="number" auto-scale tone="text-status-down" />
      </Card>
    </div>

    <!-- Containers ------------------------------------------------------ -->
    <Card :title="t('systems.containersTitle')" :description="t('systems.containersHelp')" :padded="false">
      <EmptyState
        v-if="(history?.containers ?? []).length === 0"
        :title="t('systems.containersEmptyTitle')"
        :description="t('systems.containersEmptyDescription')"
      />
      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="border-b border-border text-[11px] uppercase tracking-wide text-muted-foreground">
            <tr>
              <th class="px-4 py-2 text-start font-medium">{{ t('systems.container') }}</th>
              <th class="px-4 py-2 text-start font-medium">{{ t('systems.state') }}</th>
              <th class="px-4 py-2 text-end font-medium">CPU</th>
              <th class="px-4 py-2 text-end font-medium">{{ t('systems.memory') }}</th>
              <th class="px-4 py-2 text-end font-medium">{{ t('systems.network') }}</th>
            </tr>
          </thead>
          <tbody>
            <template v-for="container in history?.containers ?? []" :key="container.name">
              <tr
                class="cursor-pointer border-b border-border/60 last:border-0 hover:bg-accent/40"
                @click="toggleContainer(container.name)"
              >
                <td class="px-4 py-2">
                  <p class="font-medium">{{ container.name }}</p>
                  <p class="truncate text-[11px] text-muted-foreground">{{ container.image }}</p>
                </td>
                <td class="px-4 py-2">
                  <Badge :variant="containerRunning(container.status) ? 'success' : 'secondary'">
                    {{ container.status || t('common.unknown') }}
                  </Badge>
                  <Badge v-if="container.health" class="ms-1" :variant="healthVariant(container.health)">
                    {{ container.health }}
                  </Badge>
                  <Badge v-if="container.update_available" class="ms-1" variant="warning">{{ t('systems.updateAvailable') }}</Badge>
                </td>
                <td class="px-4 py-2 text-end tabular-nums">{{ formatPercent(container.cpu) }}</td>
                <td class="px-4 py-2 text-end tabular-nums">{{ formatBytes(container.mem_mb * 1024 * 1024) }}</td>
                <td class="px-4 py-2 text-end tabular-nums text-xs text-muted-foreground">
                  {{ formatBytes(container.net_sent) }} / {{ formatBytes(container.net_recv) }}
                </td>
              </tr>
              <tr v-if="selectedContainer === container.name" class="border-b border-border/60 bg-background/40">
                <td colspan="5" class="px-4 py-3">
                  <div v-if="containerLoading" class="text-xs text-muted-foreground">{{ t('common.loading') }}</div>
                  <div v-else class="grid gap-3 lg:grid-cols-3">
                    <div>
                      <p class="text-[11px] text-muted-foreground">CPU</p>
                      <MetricChart :values="containerSeries.map((point) => point.cpu)" unit="percent" :height="80" />
                    </div>
                    <div>
                      <p class="text-[11px] text-muted-foreground">{{ t('systems.memory') }}</p>
                      <MetricChart :values="containerSeries.map((point) => point.mem_mb * 1024 * 1024)" unit="bytes" :height="80" tone="text-status-degraded" />
                    </div>
                    <div>
                      <p class="text-[11px] text-muted-foreground">{{ t('systems.networkSent') }}</p>
                      <MetricChart :values="containerSeries.map((point) => point.net_sent)" unit="rate" :height="80" tone="text-status-up" />
                    </div>
                  </div>
                </td>
              </tr>
            </template>
          </tbody>
        </table>
      </div>
    </Card>

    <!-- S.M.A.R.T. ------------------------------------------------------ -->
    <Card :title="t('systems.smartTitle')" :description="t('systems.smartHelp')" :padded="false">
      <EmptyState
        v-if="disks.length === 0"
        :title="t('systems.smartEmptyTitle')"
        :description="t('systems.smartEmptyDescription')"
      />
      <div v-else class="divide-y divide-border/60">
        <div v-for="[device, disk] in disks" :key="device">
          <button
            type="button"
            class="flex w-full items-center justify-between gap-3 px-4 py-3 text-start text-sm hover:bg-accent/40"
            @click="toggleDisk(device)"
          >
            <span class="min-w-0">
              <span class="flex items-center gap-2 font-medium">
                <HardDrive class="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
                {{ disk.model_name || device }}
              </span>
              <span class="mt-0.5 block text-[11px] text-muted-foreground">
                {{ device }}<span v-if="disk.serial_number"> · {{ disk.serial_number }}</span>
                <span v-if="disk.capacity"> · {{ formatBytes(disk.capacity) }}</span>
                <span v-if="disk.temperature"> · {{ disk.temperature }} °C</span>
              </span>
            </span>
            <Badge :variant="smartVariant(disk.status)">{{ disk.status || t('common.unknown') }}</Badge>
          </button>
          <div v-if="expandedDisk === device" class="border-t border-border/60 bg-background/40 px-4 py-3">
            <table class="w-full text-xs">
              <thead class="text-[11px] uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th class="py-1 text-start font-medium">ID</th>
                  <th class="py-1 text-start font-medium">{{ t('systems.smartAttribute') }}</th>
                  <th class="py-1 text-end font-medium">{{ t('systems.smartValue') }}</th>
                  <th class="py-1 text-end font-medium">{{ t('systems.smartWorst') }}</th>
                  <th class="py-1 text-end font-medium">{{ t('systems.smartThreshold') }}</th>
                  <th class="py-1 text-end font-medium">{{ t('systems.smartRaw') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="attribute in disk.attributes ?? []" :key="`${device}-${attribute.id}-${attribute.name}`" class="border-t border-border/40">
                  <td class="py-1 tabular-nums text-muted-foreground">{{ attribute.id }}</td>
                  <td class="py-1">{{ attribute.name }}</td>
                  <td class="py-1 text-end tabular-nums">{{ attribute.value }}</td>
                  <td class="py-1 text-end tabular-nums">{{ attribute.worst }}</td>
                  <td class="py-1 text-end tabular-nums">{{ attribute.threshold }}</td>
                  <td class="py-1 text-end font-mono text-[11px] text-muted-foreground">{{ attribute.raw_string || attribute.raw_value }}</td>
                </tr>
              </tbody>
            </table>
            <p v-if="history?.smart_at" class="mt-2 text-[11px] text-muted-foreground">
              {{ t('systems.smartCollected', { time: formatDateTime(history.smart_at, locale) }) }}
            </p>
          </div>
        </div>
      </div>
    </Card>
  </div>
</template>
