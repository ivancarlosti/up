<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Copy, ListPlus, Pencil, Plus, RefreshCw, Trash2, Wand2 } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import SortHeader from '@/components/ui/SortHeader.vue'
import StatusBadge from '@/components/monitors/StatusBadge.vue'
import Switch from '@/components/ui/Switch.vue'
import ApplyTemplateDialog from '@/components/monitors/ApplyTemplateDialog.vue'
import BulkAddDialog from '@/components/monitors/BulkAddDialog.vue'
import MonitorForm from '@/components/monitors/MonitorForm.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { certificateTitle, domainTitle, expiryStateVariant, expiryVariant } from '@/lib/expiry'
import { formatInterval, formatUptime } from '@/lib/format'
import { monitorSortKeys, sortMonitors } from '@/lib/sort'
import type { MonitorSortKey, SortDirection } from '@/lib/sort'
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

const groupNames = computed(() => new Map(groups.value.map((group) => [group.id, group.name])))

/** hasCertificates reveals the validity column only when it says something. */
const hasCertificates = computed(() => monitors.value.some((monitor) => monitor.cert_watch || monitor.certificate))

/** hasDomains reveals the domain expiration column on the same rule. */
const hasDomains = computed(() => monitors.value.some((monitor) => monitor.domain_watch || monitor.domain))

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
 * Sort state of the table.
 *
 * The choice is remembered per browser (the same pattern as the theme store) so
 * an operator who always sorts by "domain" finds the table that way after a
 * reload. A hand edited or stale value falls back to the default instead of
 * breaking the page.
 */
const SORT_STORAGE_KEY = 'up.admin.monitors.sort'

function loadSort(): { key: MonitorSortKey; direction: SortDirection } {
  const fallback: { key: MonitorSortKey; direction: SortDirection } = { key: 'name', direction: 'asc' }
  try {
    const raw = localStorage.getItem(SORT_STORAGE_KEY)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as { key?: MonitorSortKey; direction?: SortDirection }
    if (!parsed.key || !monitorSortKeys.includes(parsed.key)) return fallback
    return { key: parsed.key, direction: parsed.direction === 'desc' ? 'desc' : 'asc' }
  } catch {
    return fallback
  }
}

const sort = ref(loadSort())

/** toggleSort switches the column, or flips the direction of the current one. */
function toggleSort(key: MonitorSortKey): void {
  sort.value =
    sort.value.key === key
      ? { key, direction: sort.value.direction === 'asc' ? 'desc' : 'asc' }
      : { key, direction: 'asc' }
  try {
    localStorage.setItem(SORT_STORAGE_KEY, JSON.stringify(sort.value))
  } catch {
    // A browser that refuses to persist (private mode) still sorts, it just
    // forgets the preference after a reload.
  }
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

    <EmptyState v-if="!filtered.length" :title="t('dashboard.empty')" :description="t('dashboard.emptyHint')" />

    <Card v-else :padded="false">
      <table class="data-table">
        <thead>
          <tr>
            <SortHeader
              :label="t('common.name')"
              column="name"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="toggleSort('name')"
            />
            <SortHeader
              :label="t('common.type')"
              column="type"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="toggleSort('type')"
            />
            <SortHeader
              :label="t('monitor.groupsSection')"
              column="group"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="toggleSort('group')"
            />
            <SortHeader
              v-if="hasCertificates"
              :label="t('certificate.column')"
              column="certificate"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="toggleSort('certificate')"
            />
            <SortHeader
              v-if="hasDomains"
              :label="t('domain.column')"
              column="domain"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="toggleSort('domain')"
            />
            <SortHeader
              :label="t('common.status')"
              column="status"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="toggleSort('status')"
            />
            <SortHeader
              :label="t('common.interval')"
              column="interval"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="toggleSort('interval')"
            />
            <SortHeader
              :label="t('common.uptime')"
              column="uptime"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="toggleSort('uptime')"
            />
            <th>{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="monitor in sorted" :key="monitor.id">
            <td>
              <button class="text-start hover:underline" @click="router.push({ name: 'monitor-detail', params: { id: String(monitor.id) } })">
                {{ monitor.name }}
              </button>
              <Badge v-if="monitor.template_name" variant="outline" class="ms-1">
                {{ monitor.template_name }}
              </Badge>
            </td>
            <td><Badge variant="secondary">{{ monitor.type }}</Badge></td>
            <td>
              <div class="flex flex-wrap gap-1">
                <Badge v-for="id in monitor.group_ids ?? []" :key="id" variant="outline">
                  {{ groupNames.get(id) ?? id }}
                </Badge>
                <span v-if="!(monitor.group_ids ?? []).length" class="text-muted-foreground">—</span>
              </div>
            </td>
            <td v-if="hasCertificates">
              <Badge
                v-if="monitor.certificate"
                :variant="expiryVariant(monitor.certificate.days_left)"
                :title="certificateTitle(monitor.certificate, locale)"
              >
                {{ t('certificate.daysLeft', { days: monitor.certificate.days_left }) }}
              </Badge>
              <Badge v-else-if="monitor.cert_watch" variant="secondary">{{ t('certificate.pending') }}</Badge>
              <span v-else class="text-muted-foreground">—</span>
            </td>
            <td v-if="hasDomains">
              <Badge
                v-if="monitor.domain && monitor.domain.status === 'ok'"
                :variant="expiryStateVariant(monitor.domain.status, monitor.domain.days_left)"
                :title="domainTitle(monitor.domain, locale)"
              >
                {{ t('domain.daysLeft', { days: monitor.domain.days_left }) }}
              </Badge>
              <Badge
                v-else-if="monitor.domain && monitor.domain.status === 'not_found'"
                variant="secondary"
              >
                {{ t('domain.notFound') }}
              </Badge>
              <Badge
                v-else-if="monitor.domain && monitor.domain.status === 'unsupported'"
                variant="secondary"
              >
                {{ t('domain.unsupported') }}
              </Badge>
              <Badge v-else-if="monitor.domain" variant="secondary" :title="monitor.domain.error || ''">
                {{ t('domain.unavailable') }}
              </Badge>
              <Badge v-else-if="monitor.domain_watch" variant="secondary">{{ t('domain.pending') }}</Badge>
              <span v-else class="text-muted-foreground">—</span>
            </td>
            <td><StatusBadge :status="monitor.status" /></td>
            <td>{{ formatInterval(monitor.interval_seconds) }}</td>
            <td>{{ formatUptime(monitor.uptime_24h) }}</td>
            <td>
              <div class="flex items-center gap-1">
                <Button variant="ghost" size="sm" @click="openEdit(monitor)">
                  <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button variant="ghost" size="sm" :title="t('common.clone')" :aria-label="t('common.clone')" @click="openClone(monitor)">
                  <Copy class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button
                  variant="ghost"
                  size="sm"
                  class="text-status-down"
                  @click="(pendingRemoval = monitor), (confirmOpen = true)"
                >
                  <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </Card>

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
