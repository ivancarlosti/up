<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ListPlus, Plus, RefreshCw, Tags, Wand2 } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import ApplyTemplateDialog from '@/components/monitors/ApplyTemplateDialog.vue'
import BulkAddDialog from '@/components/monitors/BulkAddDialog.vue'
import BulkTagsDialog from '@/components/monitors/BulkTagsDialog.vue'
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
import type {
  Monitor,
  MonitorCloneOptions,
  MonitorGroup,
  MonitorPayload,
  MonitorTemplate,
  Notification,
} from '@/lib/types'

/**
 * DashboardView is the one monitors screen: the actionable summary boxes on top,
 * the filters and the actions under them, and the shared monitors table below.
 *
 * It absorbed AdminMonitorsView (removed on 2026-09-27): the group / status
 * filters, the clone action and the bulk add / apply template dialogs live here
 * now, and `/admin/monitors` redirects to this route.
 */

const monitors = useMonitorStore()
const toasts = useToastStore()
const router = useRouter()
const { t, locale } = useI18n()

const notifications = ref<Notification[]>([])
const templates = ref<MonitorTemplate[]>([])
const groups = ref<MonitorGroup[]>([])
const formOpen = ref(false)
const bulkOpen = ref(false)
// The bulk tag dialog works on what is on screen (the filtered rows) or on a
// group / a tag, so it is opened from the toolbar next to the other bulk actions.
const bulkTagsOpen = ref(false)
const applyOpen = ref(false)
const editing = ref<Monitor | null>(null)
const saving = ref(false)
const confirmOpen = ref(false)
const pendingRemoval = ref<Monitor | null>(null)
const removing = ref(false)
const search = ref('')
const typeFilter = ref('')
const statusFilter = ref('')
const groupFilter = ref('')

/** Clone dialog state (the shallow monitor clone of the removed page). */
const cloneOpen = ref(false)
const cloning = ref(false)
const cloneSource = ref<Monitor | null>(null)
const cloneForm = reactive<MonitorCloneOptions>({ name: '', copy_notifications: true, copy_group: true })

/**
 * The summary box currently selected: it filters the table below, and clicking
 * the active box clears it. "all" is the neutral state.
 */
type SummaryBox = 'all' | 'up' | 'down' | 'degraded' | 'paused' | 'certificate' | 'domain'
const activeBox = ref<SummaryBox>('all')

/** Days before expiry that fill the certificate and domain boxes. */
const certificateWarnDays = 7
const domainWarnDays = 30

/** The table remembers its sort across visits (see lib/monitor-sort.ts). */
const sort = ref(loadMonitorSort())

function toggleSort(key: MonitorSortKey): void {
  sort.value = toggleMonitorSort(sort.value, key)
}

/** selectBox selects a box, or clears it when it is already active. */
function selectBox(box: SummaryBox): void {
  activeBox.value = activeBox.value === box ? 'all' : box
}

/** hasActiveFilters is true when any filter narrows the table. */
const hasActiveFilters = computed(
  () =>
    activeBox.value !== 'all' ||
    groupFilter.value !== '' ||
    typeFilter.value !== '' ||
    statusFilter.value !== '' ||
    search.value.trim() !== '',
)

/** clearFilters resets every filter back to its neutral state. */
function clearFilters(): void {
  activeBox.value = 'all'
  groupFilter.value = ''
  typeFilter.value = ''
  statusFilter.value = ''
  search.value = ''
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


/** groupFilterOptions adds the "all groups" entry to the real groups. */
const groupFilterOptions = computed(() => [
  { value: '', label: t('monitor.allGroups') },
  ...groups.value.map((group) => ({ value: String(group.id), label: `${group.name} (${group.monitor_count})` })),
])

/** typeOptions adds the "all types" entry to the probe types. */
const typeOptions = computed(() => [
  { value: '', label: t('dashboard.allTypes') },
  { value: 'http', label: t('monitor.typeHttp') },
  { value: 'keyword', label: t('monitor.typeKeyword') },
  { value: 'tcp', label: t('monitor.typeTcp') },
  { value: 'dns', label: t('monitor.typeDns') },
  { value: 'ssl', label: t('monitor.typeSsl') },
])

/**
 * statusFilterOptions adds the "all statuses" entry. "Paused" is the effective
 * status of an inactive monitor, so it is a filter of its own (the same rule the
 * header counters use: a paused monitor is never counted as up or down).
 */
const statusFilterOptions = computed(() => [
  { value: '', label: t('dashboard.allStatus') },
  { value: 'up', label: t('status.up') },
  { value: 'down', label: t('status.down') },
  { value: 'degraded', label: t('status.degraded') },
  { value: 'pending', label: t('status.pending') },
  { value: 'maintenance', label: t('status.maintenance') },
  { value: 'unknown', label: t('status.unknown') },
  { value: 'paused', label: t('common.paused') },
])

/** filtered applies the summary box, the selects and the search term together. */
const filtered = computed(() => {
  const term = search.value.trim().toLowerCase()
  const groupID = Number(groupFilter.value) || 0
  return list.value.filter((monitor) => {
    if (groupID > 0 && monitor.group_id !== groupID) return false
    if (typeFilter.value && monitor.type !== typeFilter.value) return false
    if (statusFilter.value) {
      if (statusFilter.value === 'paused') {
        if (monitor.active) return false
      } else if (!monitor.active || monitor.status !== statusFilter.value) {
        return false
      }
    }
    if (!matchesBox(monitor)) return false
    if (!term) return true
    return (
      monitor.name.toLowerCase().includes(term) ||
      monitor.description?.toLowerCase().includes(term) ||
      monitor.tags?.toLowerCase().includes(term) ||
      monitor.type.includes(term)
    )
  })
})

/** groupNames feeds both the "group" comparator and the group cells. */
const groupNames = computed(() => new Map(groups.value.map((group) => [group.id, group.name])))

const sorted = computed(() =>
  sortMonitors(filtered.value, sort.value.key, sort.value.direction, {
    groupNames: groupNames.value,
    locale: locale.value,
  }),
)

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
 * a link the selected type cannot follow; without the list the dialog could not
 * do it.
 */
async function loadTemplates(): Promise<void> {
  try {
    templates.value = await api.monitorTemplates()
  } catch {
    templates.value = []
  }
}

/** The group list names the filter options and the table cells. */
async function loadGroups(): Promise<void> {
  try {
    groups.value = await api.monitorGroups()
  } catch {
    groups.value = []
  }
}

/** refresh reloads the monitors and the group counters after a mutation. */
async function refresh(): Promise<void> {
  await Promise.all([load(), loadGroups()])
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
    if (editing.value) await api.updateMonitor(editing.value.id, payload)
    else await api.createMonitor(payload)
    formOpen.value = false
    toasts.success(t('common.saved'))
    await refresh()
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
    // Pausing or resuming moves the monitor between the summary boxes.
    monitors.refreshSummary()
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
    monitors.refreshSummary()
    toasts.success(t('common.deleted'))
    confirmOpen.value = false
    // The group counters drop the deleted monitor.
    await loadGroups()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    removing.value = false
  }
}

/** openClone prepares the clone dialog; an empty name lets the API decide. */
function openClone(monitor: Monitor): void {
  cloneSource.value = monitor
  cloneForm.name = ''
  cloneForm.copy_notifications = true
  cloneForm.copy_group = true
  cloneOpen.value = true
}

async function submitClone(): Promise<void> {
  if (!cloneSource.value) return
  cloning.value = true
  try {
    await api.cloneMonitor(cloneSource.value.id, {
      name: cloneForm.name?.trim() || undefined,
      copy_notifications: cloneForm.copy_notifications,
      copy_group: cloneForm.copy_group,
    })
    cloneOpen.value = false
    toasts.success(t('common.saved'))
    await refresh()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    cloning.value = false
  }
}

function openDetail(monitor: Monitor): void {
  router.push({ name: 'monitor-detail', params: { id: String(monitor.id) } })
}

onMounted(async () => {
  await Promise.all([load(), loadNotifications(), loadTemplates(), loadGroups()])
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
        <span class="text-[11px] text-muted-foreground">
          {{ t('common.lastCheck') }}: {{ formatRelative(monitors.summary ? new Date().toISOString() : null, locale) }}
        </span>
        <Button variant="outline" size="sm" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
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

    <!--
      The filters and the actions of the removed Admin > Monitors page, right
      below the summary boxes: the boxes answer "how many", this toolbar lets the
      operator narrow which rows that number is made of, and adds a monitor, adds
      many in bulk or applies a template.
    -->
    <div class="flex flex-wrap items-center gap-2">
      <Select v-if="groups.length" v-model="groupFilter" class="w-44" :options="groupFilterOptions" />
      <Select v-model="typeFilter" class="w-40" :options="typeOptions" />
      <Select v-model="statusFilter" class="w-40" :options="statusFilterOptions" />
      <Input v-model="search" class="max-w-xs" :placeholder="t('dashboard.searchPlaceholder')" />
      <Button v-if="hasActiveFilters" variant="outline" size="sm" @click="clearFilters">
        {{ t('dashboard.clearFilter') }}
      </Button>
      <div class="ms-auto flex flex-wrap items-center gap-2">
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('dashboard.addMonitor') }}
        </Button>
        <Button variant="outline" size="sm" @click="bulkOpen = true">
          <ListPlus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('bulk.button') }}
        </Button>
        <Button variant="outline" size="sm" @click="bulkTagsOpen = true">
          <Tags class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('bulkTags.button') }}
        </Button>
        <Button v-if="templates.length" variant="outline" size="sm" @click="applyOpen = true">
          <Wand2 class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('apply.button') }}
        </Button>
      </div>
    </div>

    <Alert v-if="degradedNodes.length" variant="warning">
      {{ t('dashboard.degradedWarning') }} ({{ degradedNodes.join(', ') }})
    </Alert>

    <MonitorTable
      :monitors="sorted"
      :sort="sort"
      :loading="monitors.loading"
      :actions="['detail', 'check', 'toggle', 'edit', 'clone', 'remove']"
      :empty-title="t('dashboard.empty')"
      :empty-description="t('dashboard.emptyHint')"
      @sort="toggleSort"
      @detail="openDetail"
      @check="checkNow"
      @toggle="toggle"
      @edit="openEdit"
      @clone="openClone"
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
      :groups="groups"
      :templates="templates"
      :saving="saving"
      @submit="submit"
    />

    <BulkAddDialog v-model="bulkOpen" :templates="templates" :groups="groups" @created="refresh" />
    <!--
      The tags of the bulk dialog are applied to what the table shows, so the
      dialog receives the filtered rows (not the whole installation): the operator
      narrows with the filters, then retags exactly what is on screen.
    -->
    <BulkTagsDialog v-model="bulkTagsOpen" :monitors="sorted" :groups="groups" @updated="refresh" />
    <ApplyTemplateDialog v-model="applyOpen" :templates="templates" :monitors="list" @applied="refresh" />

    <Dialog v-model="cloneOpen" :title="t('monitor.cloneTitle')" :description="t('monitor.cloneHelp')">
      <div class="grid gap-4">
        <div class="grid gap-1">
          <Label for="clone-name">{{ t('monitor.cloneName') }}</Label>
          <Input id="clone-name" v-model="cloneForm.name" :placeholder="cloneSource ? `${cloneSource.name} (copy)` : ''" />
        </div>
        <div class="grid gap-2">
          <Switch v-model="cloneForm.copy_notifications as boolean">{{ t('monitor.copyNotifications') }}</Switch>
          <Switch v-model="cloneForm.copy_group as boolean">{{ t('monitor.copyGroup') }}</Switch>
        </div>
      </div>
      <template #footer>
        <Button variant="outline" @click="cloneOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="cloning" @click="submitClone">{{ t('common.clone') }}</Button>
      </template>
    </Dialog>

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

