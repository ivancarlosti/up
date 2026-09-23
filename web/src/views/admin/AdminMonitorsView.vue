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
import StatusBadge from '@/components/monitors/StatusBadge.vue'
import Switch from '@/components/ui/Switch.vue'
import ApplyTemplateDialog from '@/components/monitors/ApplyTemplateDialog.vue'
import BulkAddDialog from '@/components/monitors/BulkAddDialog.vue'
import MonitorForm from '@/components/monitors/MonitorForm.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatInterval, formatUptime } from '@/lib/format'
import { useToastStore } from '@/stores/toast'
import type { Monitor, MonitorCloneOptions, MonitorGroup, MonitorPayload, MonitorTemplate, Notification } from '@/lib/types'

const { t } = useI18n()
const toasts = useToastStore()
const router = useRouter()

const monitors = ref<Monitor[]>([])
const notifications = ref<Notification[]>([])
const groups = ref<MonitorGroup[]>([])
const templates = ref<MonitorTemplate[]>([])
const search = ref('')
const groupFilter = ref('')
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

/** groupFilterOptions adds the "all groups" entry to the real groups. */
const groupFilterOptions = computed(() => [
  { value: '', label: t('monitor.allGroups') },
  ...groups.value.map((group) => ({ value: String(group.id), label: `${group.name} (${group.monitor_count})` })),
])

const filtered = computed(() => {
  const term = search.value.trim().toLowerCase()
  const groupID = Number(groupFilter.value) || 0
  return monitors.value.filter((monitor) => {
    if (groupID > 0 && !(monitor.group_ids ?? []).includes(groupID)) return false
    if (!term) return true
    return monitor.name.toLowerCase().includes(term) || monitor.type.includes(term)
  })
})

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
        <p class="text-xs text-muted-foreground">{{ t('dashboard.subtitle', { up: '-', down: '-', paused: '-' }) }}</p>
      </div>
      <div class="flex items-center gap-2">
        <Select v-if="groups.length" v-model="groupFilter" class="w-44" :options="groupFilterOptions" />
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
            <th>{{ t('common.name') }}</th>
            <th>{{ t('common.type') }}</th>
            <th>{{ t('monitor.groupsSection') }}</th>
            <th>{{ t('common.status') }}</th>
            <th>{{ t('common.interval') }}</th>
            <th>{{ t('common.uptime') }}</th>
            <th>{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="monitor in filtered" :key="monitor.id">
            <td>
              <button class="text-left hover:underline" @click="router.push({ name: 'monitor-detail', params: { id: String(monitor.id) } })">
                {{ monitor.name }}
              </button>
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
