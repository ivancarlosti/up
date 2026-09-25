<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Boxes, Copy, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import SortHeader from '@/components/ui/SortHeader.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { monitorGroupSortKeys, sortMonitorGroups } from '@/lib/sort'
import type { MonitorGroupSortKey } from '@/lib/sort'
import { loadTableSort, toggleTableSort } from '@/lib/table-sort'
import { useToastStore } from '@/stores/toast'
import type { Monitor, MonitorGroup, MonitorGroupCloneOptions, MonitorGroupPayload } from '@/lib/types'

const { t, locale } = useI18n()
const toasts = useToastStore()

const groups = ref<MonitorGroup[]>([])
const monitors = ref<Monitor[]>([])
const loading = ref(false)
const saving = ref(false)
const search = ref('')

// Filter and sort of the groups table: the text filter narrows the rows, the
// sort choice is remembered per browser (lib/table-sort.ts, the same helper the
// monitors and the expiry tables use). The default is the position the operator
// gave each group, which is the order the API returns them in.
const groupSearch = ref('')
const GROUP_SORT_STORAGE_KEY = 'up.admin.monitor-groups.sort'
const sort = ref(
  loadTableSort({
    storageKey: GROUP_SORT_STORAGE_KEY,
    keys: monitorGroupSortKeys,
    defaultKey: 'order',
  }),
)

/** toggleSort switches the column, or flips the direction of the current one. */
function toggleSort(key: MonitorGroupSortKey): void {
  sort.value = toggleTableSort(sort.value, key, GROUP_SORT_STORAGE_KEY)
}

const dialogOpen = ref(false)
const editing = ref<MonitorGroup | null>(null)
const form = reactive<MonitorGroupPayload>(blank())

const confirmOpen = ref(false)
const pendingRemoval = ref<MonitorGroup | null>(null)

const cloneOpen = ref(false)
const cloning = ref(false)
const cloneSource = ref<MonitorGroup | null>(null)
const cloneForm = reactive<MonitorGroupCloneOptions>({ name: '', deep: false, copy_links: true })

function blank(): MonitorGroupPayload {
  return { name: '', description: '', sort_order: 0, monitor_ids: [] }
}

const filteredMonitors = computed(() => {
  const term = search.value.trim().toLowerCase()
  if (!term) return monitors.value
  return monitors.value.filter((monitor) => monitor.name.toLowerCase().includes(term))
})

/** filteredGroups applies the text filter of the table (name and description). */
const filteredGroups = computed(() => {
  const term = groupSearch.value.trim().toLowerCase()
  if (!term) return groups.value
  return groups.value.filter((group) =>
    [group.name, group.description].some((value) => (value ?? '').toLowerCase().includes(term)),
  )
})

/** sortedGroups applies the chosen column to the filtered rows (see lib/sort.ts). */
const sortedGroups = computed(() =>
  sortMonitorGroups(filteredGroups.value, sort.value.key, sort.value.direction, { locale: locale.value }),
)

async function load(): Promise<void> {
  loading.value = true
  try {
    const [groupList, monitorList] = await Promise.all([api.monitorGroups(), api.monitors({ decorate: 'false' })])
    groups.value = groupList
    monitors.value = monitorList
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    loading.value = false
  }
}

function monitorName(id: number): string {
  return monitors.value.find((monitor) => monitor.id === id)?.name ?? String(id)
}

function openCreate(): void {
  editing.value = null
  Object.assign(form, blank())
  search.value = ''
  dialogOpen.value = true
}

function openEdit(group: MonitorGroup): void {
  editing.value = group
  Object.assign(form, blank(), {
    name: group.name,
    description: group.description,
    sort_order: group.sort_order,
    monitor_ids: [...(group.monitor_ids ?? [])],
  })
  search.value = ''
  dialogOpen.value = true
}

function toggleMonitor(id: number, value: boolean): void {
  const ids = new Set(form.monitor_ids ?? [])
  if (value) ids.add(id)
  else ids.delete(id)
  form.monitor_ids = [...ids]
}

async function save(): Promise<void> {
  saving.value = true
  try {
    const payload: MonitorGroupPayload = {
      name: form.name?.trim() ?? '',
      description: form.description ?? '',
      sort_order: Number(form.sort_order) || 0,
      monitor_ids: form.monitor_ids ?? [],
    }
    if (editing.value) await api.updateMonitorGroup(editing.value.id, payload)
    else await api.createMonitorGroup(payload)
    dialogOpen.value = false
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
    await api.deleteMonitorGroup(pendingRemoval.value.id)
    confirmOpen.value = false
    toasts.success(t('common.deleted'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

/** openClone prepares the clone dialog: shallow by default, it cannot duplicate data by accident. */
function openClone(group: MonitorGroup): void {
  cloneSource.value = group
  cloneForm.name = ''
  cloneForm.deep = false
  cloneForm.copy_links = true
  cloneOpen.value = true
}

async function submitClone(): Promise<void> {
  if (!cloneSource.value) return
  cloning.value = true
  try {
    await api.cloneMonitorGroup(cloneSource.value.id, {
      name: cloneForm.name?.trim() || undefined,
      deep: cloneForm.deep,
      copy_links: cloneForm.copy_links,
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
        <h1 class="text-lg font-semibold">{{ t('groups.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('groups.subtitle') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" :loading="loading" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('groups.new') }}
        </Button>
      </div>
    </header>

    <EmptyState v-if="!groups.length" :title="t('groups.empty')" :description="t('groups.emptyHint')" />

    <Card v-else :padded="false">
      <div class="flex flex-wrap items-center gap-2 border-b border-border px-5 py-4">
        <Input v-model="groupSearch" class="max-w-xs" :placeholder="t('groups.filterPlaceholder')" />
        <span class="text-[11px] text-muted-foreground">
          {{ t('common.shownOfTotal', { shown: filteredGroups.length, total: groups.length }) }}
        </span>
      </div>

      <p v-if="!filteredGroups.length" class="p-5 text-xs text-muted-foreground">
        {{ t('common.noMatch') }}
      </p>

      <div v-else class="overflow-x-auto">
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
                :label="t('groups.monitors')"
                column="monitors"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('monitors')"
              />
              <SortHeader
                :label="t('groups.sortOrder')"
                column="order"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('order')"
              />
              <th class="text-end">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="group in sortedGroups" :key="group.id">
              <td>
                <span class="flex items-center gap-2 text-sm font-medium">
                  <Boxes class="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                  {{ group.name }}
                </span>
                <span v-if="group.description" class="block text-[11px] text-muted-foreground">
                  {{ group.description }}
                </span>
              </td>
              <td>
                <div class="flex flex-wrap items-center gap-1">
                  <Badge v-for="id in group.monitor_ids" :key="id" variant="outline">{{ monitorName(id) }}</Badge>
                  <span v-if="!group.monitor_ids.length" class="text-[11px] text-muted-foreground">
                    {{ t('groups.noMonitors') }}
                  </span>
                </div>
              </td>
              <td class="whitespace-nowrap tabular-nums">{{ group.sort_order }}</td>
              <td class="text-end">
                <div class="flex items-center justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    :title="t('common.edit')"
                    :aria-label="t('common.edit')"
                    @click="openEdit(group)"
                  >
                    <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    :title="t('common.clone')"
                    :aria-label="t('common.clone')"
                    @click="openClone(group)"
                  >
                    <Copy class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="text-status-down"
                    :title="t('common.delete')"
                    :aria-label="t('common.delete')"
                    @click="(pendingRemoval = group), (confirmOpen = true)"
                  >
                    <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </Card>

    <Dialog v-model="dialogOpen" :title="editing ? t('groups.edit') : t('groups.new')" wide>
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="group-name" required>{{ t('groups.name') }}</Label>
          <Input id="group-name" v-model="form.name" />
        </div>
        <div class="grid gap-1">
          <Label for="group-order">{{ t('groups.sortOrder') }}</Label>
          <Input id="group-order" v-model="form.sort_order" type="number" min="0" />
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="group-description">{{ t('common.description') }}</Label>
          <Textarea id="group-description" v-model="form.description" :rows="2" />
        </div>
        <div class="grid gap-2 sm:col-span-2">
          <Label :help="t('groups.monitorsHelp')">{{ t('groups.monitors') }}</Label>
          <Input id="group-search" v-model="search" :placeholder="t('groups.searchMonitors')" />
          <div class="flex flex-wrap gap-4">
            <Checkbox
              v-for="monitor in filteredMonitors"
              :key="monitor.id"
              :model-value="(form.monitor_ids ?? []).includes(monitor.id)"
              @update:model-value="toggleMonitor(monitor.id, $event)"
            >
              {{ monitor.name }}
            </Checkbox>
          </div>
        </div>
      </div>

      <template #footer>
        <Button variant="outline" @click="dialogOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="save">{{ t('common.save') }}</Button>
      </template>
    </Dialog>

    <Dialog v-model="cloneOpen" :title="t('groups.cloneTitle')">
      <div class="grid gap-4">
        <div class="grid gap-1">
          <Label for="group-clone-name">{{ t('groups.cloneName') }}</Label>
          <Input
            id="group-clone-name"
            v-model="cloneForm.name"
            :placeholder="cloneSource ? `${cloneSource.name} (copy)` : ''"
          />
        </div>
        <div class="grid gap-2">
          <Switch v-model="cloneForm.deep as boolean">{{ t('groups.cloneDeep') }}</Switch>
          <p class="text-[11px] text-muted-foreground">{{ t('groups.cloneDeepHelp') }}</p>
          <Switch v-if="cloneForm.deep" v-model="cloneForm.copy_links as boolean">
            {{ t('groups.copyLinks') }}
          </Switch>
        </div>
      </div>
      <template #footer>
        <Button variant="outline" @click="cloneOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="cloning" @click="submitClone">{{ t('common.clone') }}</Button>
      </template>
    </Dialog>

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('groups.deleteTitle')"
      :description="t('groups.deleteWarning')"
      :confirm-label="t('common.delete')"
      @confirm="confirmRemove"
    />
  </div>
</template>
