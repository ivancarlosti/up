<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ListPlus, Plus, RefreshCw, Wand2 } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import ApplyTemplateDialog from '@/components/monitors/ApplyTemplateDialog.vue'
import BulkAddDialog from '@/components/monitors/BulkAddDialog.vue'
import MonitorForm from '@/components/monitors/MonitorForm.vue'
import MonitorTable from '@/components/monitors/MonitorTable.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { loadMonitorSort, toggleMonitorSort } from '@/lib/monitor-sort'
import { sortMonitors } from '@/lib/sort'
import type { MonitorSortKey } from '@/lib/sort'
import { useToastStore } from '@/stores/toast'
import type {
  Monitor,
  MonitorCloneOptions,
  MonitorGroup,
  MonitorPayload,
  MonitorTemplate,
  Notification,
} from '@/lib/types'

const { t, locale } = useI18n()
const toasts = useToastStore()
const router = useRouter()

const monitors = ref<Monitor[]>([])
const notifications = ref<Notification[]>([])
const groups = ref<MonitorGroup[]>([])
const templates = ref<MonitorTemplate[]>([])
const search = ref('')
const groupFilter = ref('')
const typeFilter = ref('')
const statusFilter = ref('')
const formOpen = ref(false)
const bulkOpen = ref(false)
const applyOpen = ref(false)
const editing = ref<Monitor | null>(null)
const saving = ref(false)
const confirmOpen = ref(false)
const pendingRemoval = ref<Monitor | null>(null)

const cloneOpen = ref(false)
const cloning = ref(false)
const cloneSource = ref<Monitor | null>(null)
const cloneForm = reactive<MonitorCloneOptions>({ name: '', copy_notifications: true, copy_groups: true })

/**
 * counts feeds the header subtitle. It applies the same rule as the server
 * (internal/handlers/public.go): a paused monitor is counted as paused, never
 * as up/down, and the rest follows the aggregated status of the monitor.
 */
const counts = computed(() => {
  const counters = { up: 0, down: 0, paused: 0 }
  for (const monitor of monitors.value) {
    if (!monitor.active) {
      counters.paused += 1
      continue
    }
    if (monitor.status === 'up') counters.up += 1
    else if (monitor.status === 'down') counters.down += 1
  }
  return counters
})

/**
 * Sort state of the table. The persistence lives in lib/monitor-sort.ts so the
 * dashboard table keeps the exact same preference.
 */
const sort = ref(loadMonitorSort())

/** toggleSort switches the column, or flips the direction of the current one. */
function toggleSort(key: MonitorSortKey): void {
  sort.value = toggleMonitorSort(sort.value, key)
}

/** groupFilterOptions adds the "all groups" entry to the real groups. */
const groupFilterOptions = computed(() => [
  { value: '', label: t('monitor.allGroups') },
  ...groups.value.map((group) => ({ value: String(group.id), label: `${group.name} (${group.monitor_count})` })),
])

/** typeFilterOptions adds the "all types" entry to the probe types. */
const typeFilterOptions = computed(() => [
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

const filtered = computed(() => {
  const term = search.value.trim().toLowerCase()
  const groupID = Number(groupFilter.value) || 0
  return monitors.value.filter((monitor) => {
    if (groupID > 0 && !(monitor.group_ids ?? []).includes(groupID)) return false
    if (typeFilter.value && monitor.type !== typeFilter.value) return false
    if (statusFilter.value) {
      if (statusFilter.value === 'paused') {
        if (monitor.active) return false
      } else if (!monitor.active || monitor.status !== statusFilter.value) {
        return false
      }
    }
    if (!term) return true
    return monitor.name.toLowerCase().includes(term) || monitor.type.includes(term)
  })
})

/**
 * Group names feed the "group" comparator of the table (the cells themselves
 * resolve them inside MonitorTable).
 */
const groupNames = computed(() => new Map(groups.value.map((group) => [group.id, group.name])))

/** sorted applies the chosen column to the filtered rows (see lib/sort.ts). */
const sorted = computed(() =>
  sortMonitors(filtered.value, sort.value.key, sort.value.direction, {
    groupNames: groupNames.value,
    locale: locale.value,
  }),
)

async function load(): Promise<void> {
  try {
    const [list, channels, groupList, templateList] = await Promise.all([
      api.monitors(),
      api.notifications(),
      api.monitorGroups(),
      api.monitorTemplates(),
    ])
    monitors.value = list
    notifications.value = channels
    groups.value = groupList
    templates.value = templateList
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
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

/** openDetail navigates to the monitor detail page. */
function openDetail(monitor: Monitor): void {
  router.push({ name: 'monitor-detail', params: { id: String(monitor.id) } })
}

/** askRemove opens the delete confirmation for a monitor. */
function askRemove(monitor: Monitor): void {
  pendingRemoval.value = monitor
  confirmOpen.value = true
}

async function submit(payload: MonitorPayload): Promise<void> {
  saving.value = true
  try {
    if (editing.value) await api.updateMonitor(editing.value.id, payload)
    else await api.createMonitor(payload)
    formOpen.value = false
    toasts.success(t('common.saved'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

async function confirmRemove(): Promise<void> {
  if (!pendingRemoval.value) return
  try {
    await api.deleteMonitor(pendingRemoval.value.id)
    confirmOpen.value = false
    toasts.success(t('common.deleted'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

/** openClone prepares the clone dialog; an empty name lets the API decide. */
function openClone(monitor: Monitor): void {
  cloneSource.value = monitor
  cloneForm.name = ''
  cloneForm.copy_notifications = true
  cloneForm.copy_groups = true
  cloneOpen.value = true
}

async function submitClone(): Promise<void> {
  if (!cloneSource.value) return
  cloning.value = true
  try {
    await api.cloneMonitor(cloneSource.value.id, {
      name: cloneForm.name?.trim() || undefined,
      copy_notifications: cloneForm.copy_notifications,
      copy_groups: cloneForm.copy_groups,
    })
    cloneOpen.value = false
    toasts.success(t('common.saved'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    cloning.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col gap-4">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold">{{ t('nav.monitors') }}</h1>
        <p class="text-xs text-muted-foreground">
          {{ t('dashboard.subtitle', { up: counts.up, down: counts.down, paused: counts.paused }) }}
        </p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <Select v-if="groups.length" v-model="groupFilter" class="w-44" :options="groupFilterOptions" />
        <Select v-model="typeFilter" class="w-40" :options="typeFilterOptions" />
        <Select v-model="statusFilter" class="w-40" :options="statusFilterOptions" />
        <Input v-model="search" class="max-w-xs" :placeholder="t('dashboard.searchPlaceholder')" />
        <Button variant="outline" size="sm" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('dashboard.addMonitor') }}
        </Button>
        <Button variant="outline" size="sm" @click="bulkOpen = true">
          <ListPlus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('bulk.button') }}
        </Button>
        <Button v-if="templates.length" variant="outline" size="sm" @click="applyOpen = true">
          <Wand2 class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('apply.button') }}
        </Button>
      </div>
    </header>

    <MonitorTable
      :monitors="sorted"
      :groups="groups"
      :sort="sort"
      :actions="['detail', 'edit', 'clone', 'remove']"
      :empty-title="t('dashboard.empty')"
      :empty-description="t('dashboard.emptyHint')"
      @sort="toggleSort"
      @detail="openDetail"
      @edit="openEdit"
      @clone="openClone"
      @remove="askRemove"
    />

    <MonitorForm
      v-model="formOpen"
      :monitor="editing"
      :notifications="notifications"
      :groups="groups"
      :templates="templates"
      :saving="saving"
      @submit="submit"
    />

    <BulkAddDialog v-model="bulkOpen" :templates="templates" :groups="groups" @created="load" />
    <ApplyTemplateDialog v-model="applyOpen" :templates="templates" :monitors="monitors" @applied="load" />

    <Dialog v-model="cloneOpen" :title="t('monitor.cloneTitle')" :description="t('monitor.cloneHelp')">
      <div class="grid gap-4">
        <div class="grid gap-1">
          <Label for="clone-name">{{ t('monitor.cloneName') }}</Label>
          <Input id="clone-name" v-model="cloneForm.name" :placeholder="cloneSource ? `${cloneSource.name} (copy)` : ''" />
        </div>
        <div class="grid gap-2">
          <Switch v-model="cloneForm.copy_notifications as boolean">{{ t('monitor.copyNotifications') }}</Switch>
          <Switch v-model="cloneForm.copy_groups as boolean">{{ t('monitor.copyGroups') }}</Switch>
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
      @confirm="confirmRemove"
    />
  </div>
</template>
