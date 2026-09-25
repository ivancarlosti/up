<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, RefreshCw } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Input from '@/components/ui/Input.vue'
import Select from '@/components/ui/Select.vue'
import MonitorForm from '@/components/monitors/MonitorForm.vue'
import MonitorTable from '@/components/monitors/MonitorTable.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatRelative } from '@/lib/format'
import { loadMonitorSort, toggleMonitorSort } from '@/lib/monitor-sort'
import { sortMonitors } from '@/lib/sort'
import type { MonitorSortKey } from '@/lib/sort'
import { useMonitorStore } from '@/stores/monitors'
import { useToastStore } from '@/stores/toast'
import type { Monitor, MonitorPayload, MonitorTemplate, Notification } from '@/lib/types'

const monitors = useMonitorStore()
const toasts = useToastStore()
const router = useRouter()
const { t, locale } = useI18n()

const notifications = ref<Notification[]>([])
const templates = ref<MonitorTemplate[]>([])
const formOpen = ref(false)
const editing = ref<Monitor | null>(null)
const saving = ref(false)
const confirmOpen = ref(false)
const pendingRemoval = ref<Monitor | null>(null)
const removing = ref(false)
const search = ref('')
const typeFilter = ref('')

/**
 * The summary box currently selected: it filters the table below, and clicking
 * the active box clears it. "all" is the neutral state.
 */
type SummaryBox = 'all' | 'up' | 'down' | 'degraded' | 'paused' | 'certificate' | 'domain'
const activeBox = ref<SummaryBox>('all')

/** Days before expiry that fill the certificate and domain boxes. */
const certificateWarnDays = 7
const domainWarnDays = 30

/** The table shares its sort preference with Admin > Monitors. */
const sort = ref(loadMonitorSort())

function toggleSort(key: MonitorSortKey): void {
  sort.value = toggleMonitorSort(sort.value, key)
}

/** selectBox selects a box, or clears it when it is already active. */
function selectBox(box: SummaryBox): void {
  activeBox.value = activeBox.value === box ? 'all' : box
}

const list = computed(() => monitors.monitors)

/** certificatesExpiring counts the certificates that expire within the window. */
const certificatesExpiring = computed(
  () =>
    list.value.filter(
      (monitor) => typeof monitor.certificate?.days_left === 'number' && monitor.certificate.days_left <= certificateWarnDays,
    ).length,
)

/** domainsExpiring counts the domains that expire within the window (or already did). */
const domainsExpiring = computed(
  () =>
    list.value.filter((monitor) => {
      if (monitor.domain?.status !== 'ok' || typeof monitor.domain.days_left !== 'number') return false
      // A negative value is an expired domain: it must be counted too.
      return monitor.domain.days_left <= domainWarnDays
    }).length,
)

/** matchesBox applies the selected summary box to one monitor. */
function matchesBox(monitor: Monitor): boolean {
  switch (activeBox.value) {
    case 'up':
      return monitor.active && monitor.status === 'up'
    case 'down':
      return monitor.active && monitor.status === 'down'
    case 'degraded':
      return monitor.active && monitor.status === 'degraded'
    case 'paused':
      return !monitor.active
    case 'certificate':
      return typeof monitor.certificate?.days_left === 'number' && monitor.certificate.days_left <= certificateWarnDays
    case 'domain':
      return (
        monitor.domain?.status === 'ok' &&
        typeof monitor.domain.days_left === 'number' &&
        monitor.domain.days_left <= domainWarnDays
      )
    default:
      return true
  }
}

/**
 * The summary boxes. The status counters and the two expiry counters double as
 * filters, so the table below always answers "what is inside this number?".
 */
const boxes = computed(() => [
  { key: 'all' as SummaryBox, label: t('dashboard.statTotal'), value: list.value.length, tone: 'text-foreground', caption: '' },
  {
    key: 'up' as SummaryBox,
    label: t('dashboard.statUp'),
    value: list.value.filter((m) => m.active && m.status === 'up').length,
    tone: 'text-status-up',
    caption: '',
  },
  {
    key: 'down' as SummaryBox,
    label: t('dashboard.statDown'),
    value: list.value.filter((m) => m.active && m.status === 'down').length,
    tone: 'text-status-down',
    caption: '',
  },
  {
    key: 'degraded' as SummaryBox,
    label: t('dashboard.statDegraded'),
    value: list.value.filter((m) => m.active && m.status === 'degraded').length,
    tone: 'text-status-degraded',
    caption: '',
  },
  {
    key: 'paused' as SummaryBox,
    label: t('dashboard.statPaused'),
    value: list.value.filter((m) => !m.active).length,
    tone: 'text-muted-foreground',
    caption: '',
  },
  {
    key: 'certificate' as SummaryBox,
    label: t('dashboard.statCertificates'),
    value: certificatesExpiring.value,
    tone: 'text-status-degraded',
    caption: t('dashboard.statCertificatesHint', { days: certificateWarnDays }),
  },
  {
    key: 'domain' as SummaryBox,
    label: t('dashboard.statDomains'),
    value: domainsExpiring.value,
    tone: 'text-status-down',
    caption: t('dashboard.statDomainsHint', { days: domainWarnDays }),
  },
])

const filtered = computed(() => {
  const term = search.value.trim().toLowerCase()
  return list.value.filter((monitor) => {
    if (typeFilter.value && monitor.type !== typeFilter.value) return false
    if (!matchesBox(monitor)) return false
    if (!term) return true
    return (
      monitor.name.toLowerCase().includes(term) ||
      monitor.description?.toLowerCase().includes(term) ||
      monitor.tags?.toLowerCase().includes(term)
    )
  })
})

const sorted = computed(() =>
  sortMonitors(filtered.value, sort.value.key, sort.value.direction, {
    // The dashboard does not load the group list; the group column falls back to
    // the name order, which is what the admin table does too for ungrouped rows.
    groupNames: new Map<number, string>(),
    locale: locale.value,
  }),
)

const typeOptions = computed(() => [
  { value: '', label: t('dashboard.allTypes') },
  { value: 'http', label: t('monitor.typeHttp') },
  { value: 'keyword', label: t('monitor.typeKeyword') },
  { value: 'tcp', label: t('monitor.typeTcp') },
  { value: 'dns', label: t('monitor.typeDns') },
])

const degradedNodes = computed(() => monitors.summary?.cluster?.offline_node_names ?? [])

async function load(): Promise<void> {
  try {
    await monitors.load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function loadNotifications(): Promise<void> {
  try {
    notifications.value = await api.notifications()
  } catch {
    notifications.value = []
  }
}

/**
 * The monitor form resolves the type of the template a monitor follows to drop
 * a link the selected type cannot follow; without the list the dashboard dialog
 * could not do it.
 */
async function loadTemplates(): Promise<void> {
  try {
    templates.value = await api.monitorTemplates()
  } catch {
    templates.value = []
  }
}

function openCreate(): void {
  editing.value = null
  formOpen.value = true
}

function openEdit(monitor: Monitor): void {
  editing.value = monitor
  formOpen.value = true
}

async function submit(payload: MonitorPayload): Promise<void> {
  saving.value = true
  try {
    const saved = editing.value
      ? await api.updateMonitor(editing.value.id, payload)
      : await api.createMonitor(payload)
    monitors.upsert(saved)
    formOpen.value = false
    toasts.success(t('common.saved'))
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

async function toggle(monitor: Monitor): Promise<void> {
  try {
    const updated = monitor.active ? await api.pauseMonitor(monitor.id) : await api.resumeMonitor(monitor.id)
    monitors.upsert(updated)
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function checkNow(monitor: Monitor): Promise<void> {
  try {
    await api.checkMonitor(monitor.id)
    toasts.info(t('dashboard.checkNow'), monitor.name)
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

function askRemove(monitor: Monitor): void {
  pendingRemoval.value = monitor
  confirmOpen.value = true
}

async function confirmRemove(): Promise<void> {
  if (!pendingRemoval.value) return
  removing.value = true
  try {
    await api.deleteMonitor(pendingRemoval.value.id)
    monitors.remove(pendingRemoval.value.id)
    toasts.success(t('common.deleted'))
    confirmOpen.value = false
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    removing.value = false
  }
}

function openDetail(monitor: Monitor): void {
  router.push({ name: 'monitor-detail', params: { id: String(monitor.id) } })
}

onMounted(async () => {
  await Promise.all([load(), loadNotifications(), loadTemplates()])
})
</script>

<template>
  <div class="flex flex-col gap-5">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold">{{ t('dashboard.title') }}</h1>
        <p class="text-xs text-muted-foreground">
          {{
            t('dashboard.subtitle', {
              up: monitors.summary?.up ?? 0,
              down: monitors.summary?.down ?? 0,
              paused: monitors.summary?.paused ?? 0,
            })
          }}
        </p>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('dashboard.addMonitor') }}
        </Button>
      </div>
    </header>

    <div class="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-7">
      <button
        v-for="box in boxes"
        :key="box.key"
        type="button"
        class="rounded-xl border px-4 py-3.5 text-start transition-colors"
        :class="activeBox === box.key ? 'border-primary bg-primary/5' : 'border-border bg-card hover:border-primary/40'"
        :aria-pressed="activeBox === box.key"
        @click="selectBox(box.key)"
      >
        <span class="block text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ box.label }}</span>
        <span class="mt-2 block text-xl font-semibold leading-none tabular-nums" :class="box.tone">{{ box.value }}</span>
        <span v-if="box.caption" class="mt-1 block text-[10px] text-muted-foreground">{{ box.caption }}</span>
      </button>
    </div>

    <Alert v-if="degradedNodes.length" variant="warning">
      {{ t('dashboard.degradedWarning') }} ({{ degradedNodes.join(', ') }})
    </Alert>

    <div class="flex flex-wrap items-center gap-2">
      <Input v-model="search" class="max-w-xs" :placeholder="t('dashboard.searchPlaceholder')" />
      <Select v-model="typeFilter" :options="typeOptions" class="max-w-[12rem]" />
      <Button v-if="activeBox !== 'all'" variant="outline" size="sm" @click="activeBox = 'all'">
        {{ t('dashboard.clearFilter') }}
      </Button>
      <span class="ms-auto text-[11px] text-muted-foreground">
        {{ t('common.lastCheck') }}: {{ formatRelative(monitors.summary ? new Date().toISOString() : null, locale) }}
      </span>
    </div>

    <MonitorTable
      :monitors="sorted"
      :sort="sort"
      :loading="monitors.loading"
      :actions="['detail', 'check', 'toggle', 'edit', 'remove']"
      :empty-title="t('dashboard.empty')"
      :empty-description="t('dashboard.emptyHint')"
      @sort="toggleSort"
      @detail="openDetail"
      @check="checkNow"
      @toggle="toggle"
      @edit="openEdit"
      @remove="askRemove"
    >
      <template #empty>
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('dashboard.addMonitor') }}
        </Button>
      </template>
    </MonitorTable>

    <MonitorForm
      v-model="formOpen"
      :monitor="editing"
      :notifications="notifications"
      :templates="templates"
      :saving="saving"
      @submit="submit"
    />

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('monitor.deleteTitle')"
      :description="t('monitor.deleteWarning')"
      :confirm-label="t('common.delete')"
      :loading="removing"
      @confirm="confirmRemove"
    />
  </div>
</template>

