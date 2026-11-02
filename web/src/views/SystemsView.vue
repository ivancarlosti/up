<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import {
  Container as ContainerIcon,
  HardDrive,
  KeyRound,
  Pencil,
  Plus,
  RefreshCw,
  RotateCcw,
  Search,
  Server,
  Trash2,
} from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import SystemDialog from '@/components/systems/SystemDialog.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatBytes, formatDateTime, formatPercent } from '@/lib/format'
import { containerRunning, useSystemStore } from '@/stores/systems'
import { useToastStore } from '@/stores/toast'
import type { System, SystemPointOverview } from '@/lib/types-systems'

/**
 * SystemsView is the hardware monitoring page: the machines reported by the Beszel
 * agents, every container of every machine, and the SMART reports.
 *
 * The three tabs are one page (and one sidebar entry) because they are three views
 * of the same data: a disk belongs to a machine, a container belongs to a machine,
 * and an operator looking for "what is using the CPU" wants to move between them
 * without losing the context.
 */
const { t, locale } = useI18n()
const store = useSystemStore()
const toasts = useToastStore()

type Tab = 'systems' | 'containers' | 'smart'
const tab = ref<Tab>('systems')
const search = ref('')
const overview = ref<Map<number, SystemPointOverview>>(new Map())

const dialogOpen = ref(false)
const editing = ref<System | null>(null)
const confirmOpen = ref(false)
const pendingRemoval = ref<System | null>(null)
const rotateOpen = ref(false)
const rotateTarget = ref<System | null>(null)
const rotatedToken = ref<string | null>(null)

const tabs = computed(() => [
  { key: 'systems' as Tab, label: t('systems.tabSystems'), icon: Server, count: store.systems.length },
  { key: 'containers' as Tab, label: t('systems.tabContainers'), icon: ContainerIcon, count: store.containers.length },
  { key: 'smart' as Tab, label: t('systems.tabSmart'), icon: HardDrive, count: store.smart.length },
])

/** overviewOf returns the last sample of a machine (empty while none exists). */
function overviewOf(id: number): SystemPointOverview | undefined {
  return overview.value.get(id)
}

/** matches is the search predicate shared by the three tabs. */
function matches(values: (string | undefined | null)[], term: string): boolean {
  if (!term) return true
  return values.some((value) => (value ?? '').toLowerCase().includes(term))
}

const filteredSystems = computed(() => {
  const term = search.value.trim().toLowerCase()
  return store.systems.filter((system) => matches([system.name, system.host, system.hostname, system.os_name], term))
})

const filteredContainers = computed(() => {
  const term = search.value.trim().toLowerCase()
  return store.containers.filter((container) =>
    matches([container.name, container.image, container.system_name, container.status], term),
  )
})

const filteredSmart = computed(() => {
  const term = search.value.trim().toLowerCase()
  return store.smart.filter(
    (report) =>
      matches([report.system_name], term) ||
      Object.values(report.disks ?? {}).some((disk) =>
        matches([disk.model_name, disk.serial_number, disk.disk_name], term),
      ),
  )
})

/** statusVariant maps a system status to the badge colour. */
function statusVariant(status: System['status']): 'success' | 'danger' | 'warning' {
  if (status === 'up') return 'success'
  if (status === 'down') return 'danger'
  return 'warning'
}

/** smartVariant colours a disk health verdict. */
function smartVariant(status?: string): 'success' | 'danger' | 'warning' {
  const verdict = (status ?? '').toUpperCase()
  if (verdict === 'PASSED' || verdict === 'OK' || verdict === 'GOOD') return 'success'
  if (verdict === '') return 'warning'
  return 'danger'
}

/**
 * healthVariant colours a container health verdict.
 *
 * It is a different vocabulary from S.M.A.R.T. (Docker says healthy / starting /
 * unhealthy), so folding the two into one helper painted every healthy container
 * red.
 */
function healthVariant(health?: string): 'success' | 'danger' | 'warning' {
  const verdict = (health ?? '').toLowerCase()
  if (verdict === 'healthy') return 'success'
  if (verdict === 'starting') return 'warning'
  return 'danger'
}
/** loadTab fetches what the active tab shows. */
async function loadTab(): Promise<void> {
  if (tab.value === 'containers') return store.loadContainers()
  if (tab.value === 'smart') return store.loadSmart()
  await Promise.all([store.load(), loadOverview()])
}

/** loadOverview fetches the last sample of every machine. */
async function loadOverview(): Promise<void> {
  try {
    const rows = await api.systemsOverview()
    overview.value = new Map(rows.map((row) => [row.system_id, row]))
  } catch {
    overview.value = new Map()
  }
}

function switchTab(next: Tab): void {
  tab.value = next
  search.value = ''
  store.stopPolling()
  void loadTab()
  store.startPolling(next)
}

function openCreate(): void {
  editing.value = null
  dialogOpen.value = true
}

function openEdit(system: System): void {
  editing.value = system
  dialogOpen.value = true
}

function askRemoval(system: System): void {
  pendingRemoval.value = system
  confirmOpen.value = true
}

async function confirmRemoval(): Promise<void> {
  const system = pendingRemoval.value
  if (!system) return
  try {
    await store.remove(system.id)
    toasts.success(t('systems.deleted', { name: system.name }))
  } catch (cause) {
    toasts.error(t('systems.deleteFailed'), translateError(cause))
  } finally {
    confirmOpen.value = false
    pendingRemoval.value = null
  }
}

function askRotate(system: System): void {
  rotateTarget.value = system
  rotatedToken.value = null
  rotateOpen.value = true
}

/** confirmRotate replaces the token and shows it once. */
async function confirmRotate(): Promise<void> {
  const system = rotateTarget.value
  if (!system) return
  try {
    const rotated = await store.rotateToken(system.id)
    rotatedToken.value = rotated.token ?? null
    toasts.success(t('systems.tokenRotated'))
  } catch (cause) {
    toasts.error(t('systems.rotateFailed'), translateError(cause))
  }
}

onMounted(async () => {
  // The counters come with the page so the tab labels never advertise an empty
  // instance while the data is one click away.
  await Promise.all([loadTab(), store.loadCounters()])
  store.startPolling('systems')
})

/** copyRotated copies the freshly rotated token (it is shown once). */
async function copyRotated(): Promise<void> {
  if (!rotatedToken.value) return
  try {
    await navigator.clipboard.writeText(rotatedToken.value)
    toasts.success(t('common.copied'))
  } catch {
    toasts.error(t('common.copyFailed'))
  }
}

onBeforeUnmount(() => store.stopPolling())
</script>

<template>
  <div class="space-y-5">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold tracking-tight">{{ t('systems.title') }}</h1>
        <p class="mt-1 text-xs text-muted-foreground">{{ t('systems.subtitle') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" :loading="store.loading" @click="loadTab">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('systems.addSystem') }}
        </Button>
      </div>
    </header>

    <!-- The tabs are buttons, not routes: the three views share the search box and
         the refresh, and switching between them must not reload the page. -->
    <div class="flex flex-wrap items-center gap-2 border-b border-border pb-2">
      <button
        v-for="item in tabs"
        :key="item.key"
        type="button"
        :class="[
          'inline-flex items-center gap-2 rounded-md px-3 py-1.5 text-sm transition-colors',
          tab === item.key ? 'bg-accent font-medium text-foreground' : 'text-muted-foreground hover:bg-accent/60',
        ]"
        @click="switchTab(item.key)"
      >
        <component :is="item.icon" class="h-4 w-4" aria-hidden="true" />
        {{ item.label }}
        <span class="rounded-full bg-muted px-1.5 text-[11px] text-muted-foreground">{{ item.count }}</span>
      </button>
      <div class="relative ms-auto w-full sm:w-64">
        <Search class="pointer-events-none absolute inset-y-0 start-2 my-auto h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
        <Input v-model="search" class="ps-7" :placeholder="t('common.search')" />
      </div>
    </div>

    <p v-if="store.lastError" class="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-xs text-destructive">
      {{ store.lastError }}
    </p>

    <!-- Machines ------------------------------------------------------- -->
    <section v-if="tab === 'systems'" class="space-y-4">
      <div class="grid gap-3 sm:grid-cols-3">
        <Card>
          <p class="text-xs text-muted-foreground">{{ t('systems.summaryUp') }}</p>
          <p class="mt-1 text-2xl font-semibold text-status-up">{{ store.upCount }}</p>
        </Card>
        <Card>
          <p class="text-xs text-muted-foreground">{{ t('systems.summaryPending') }}</p>
          <p class="mt-1 text-2xl font-semibold text-status-degraded">{{ store.pendingCount }}</p>
        </Card>
        <Card>
          <p class="text-xs text-muted-foreground">{{ t('systems.summaryDown') }}</p>
          <p class="mt-1 text-2xl font-semibold text-status-down">{{ store.downCount }}</p>
        </Card>
      </div>

      <EmptyState
        v-if="!store.loading && filteredSystems.length === 0"
        :title="t('systems.emptyTitle')"
        :description="t('systems.emptyDescription')"
      >
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('systems.addSystem') }}
        </Button>
      </EmptyState>

      <div v-else class="grid gap-3 lg:grid-cols-2 2xl:grid-cols-3">
        <Card v-for="system in filteredSystems" :key="system.id" :padded="false">
          <div class="flex items-start justify-between gap-3 border-b border-border px-4 py-3">
            <div class="min-w-0">
              <RouterLink
                :to="{ name: 'system-detail', params: { id: system.id } }"
                class="truncate text-sm font-semibold hover:underline"
              >
                {{ system.name }}
              </RouterLink>
              <p class="mt-0.5 truncate text-[11px] text-muted-foreground">
                {{ system.hostname || system.host || t('systems.noHost') }}
                <span v-if="system.os_name"> · {{ system.os_name }}</span>
                <span v-if="system.agent_version"> · agent {{ system.agent_version }}</span>
              </p>
            </div>
            <Badge :variant="statusVariant(system.status)">{{ t(`status.${system.status}`) }}</Badge>
          </div>

          <div class="grid grid-cols-2 gap-2 px-4 py-3 text-xs sm:grid-cols-4">
            <div>
              <p class="text-[11px] text-muted-foreground">CPU</p>
              <p class="font-medium">{{ formatPercent(overviewOf(system.id)?.cpu) }}</p>
            </div>
            <div>
              <p class="text-[11px] text-muted-foreground">{{ t('systems.memory') }}</p>
              <p class="font-medium">{{ formatPercent(overviewOf(system.id)?.mem_pct) }}</p>
            </div>
            <div>
              <p class="text-[11px] text-muted-foreground">{{ t('systems.disk') }}</p>
              <p class="font-medium">{{ formatPercent(overviewOf(system.id)?.disk_pct) }}</p>
            </div>
            <div>
              <p class="text-[11px] text-muted-foreground">{{ t('systems.containersLabel') }}</p>
              <p class="font-medium">{{ overviewOf(system.id)?.containers ?? 0 }}</p>
            </div>
          </div>

          <div class="flex items-center justify-between gap-2 border-t border-border px-4 py-2">
            <p class="truncate text-[11px] text-muted-foreground">
              <template v-if="system.last_sample_at">
                {{ t('systems.lastSample', { time: formatDateTime(system.last_sample_at, locale) }) }}
              </template>
              <template v-else>{{ t('systems.noSampleYet') }}</template>
              <span v-if="system.owner_node_name"> · {{ t('systems.ownerNode', { node: system.owner_node_name }) }}</span>
            </p>
            <div class="flex shrink-0 items-center gap-1">
              <Button variant="ghost" size="icon" :aria-label="t('common.edit')" @click="openEdit(system)">
                <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
              </Button>
              <Button variant="ghost" size="icon" :aria-label="t('systems.rotateToken')" @click="askRotate(system)">
                <KeyRound class="h-3.5 w-3.5" aria-hidden="true" />
              </Button>
              <Button variant="ghost" size="icon" :aria-label="t('common.delete')" @click="askRemoval(system)">
                <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
              </Button>
            </div>
          </div>

          <p v-if="system.last_error" class="border-t border-border px-4 py-2 text-[11px] text-destructive">
            {{ system.last_error }}
          </p>
        </Card>
      </div>
    </section>

    <!-- Containers ----------------------------------------------------- -->
    <Card v-else-if="tab === 'containers'" :padded="false">
      <EmptyState
        v-if="!store.loading && filteredContainers.length === 0"
        :title="t('systems.containersEmptyTitle')"
        :description="t('systems.containersEmptyDescription')"
      />
      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead class="border-b border-border text-start text-[11px] uppercase tracking-wide text-muted-foreground">
            <tr>
              <th class="px-4 py-2 text-start font-medium">{{ t('systems.container') }}</th>
              <th class="px-4 py-2 text-start font-medium">{{ t('systems.system') }}</th>
              <th class="px-4 py-2 text-start font-medium">{{ t('systems.state') }}</th>
              <th class="px-4 py-2 text-end font-medium">CPU</th>
              <th class="px-4 py-2 text-end font-medium">{{ t('systems.memory') }}</th>
              <th class="px-4 py-2 text-end font-medium">{{ t('systems.network') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="container in filteredContainers" :key="`${container.system_id}-${container.name}`" class="border-b border-border/60 last:border-0">
              <td class="px-4 py-2">
                <RouterLink
                  :to="{ name: 'system-detail', params: { id: container.system_id } }
                  "
                  class="font-medium hover:underline"
                >
                  {{ container.name }}
                </RouterLink>
                <p class="truncate text-[11px] text-muted-foreground">{{ container.image }}</p>
              </td>
              <td class="px-4 py-2 text-xs text-muted-foreground">{{ container.system_name }}</td>
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
          </tbody>
        </table>
      </div>
    </Card>

    <!-- S.M.A.R.T. ----------------------------------------------------- -->
    <section v-else class="space-y-3">
      <EmptyState
        v-if="!store.loading && filteredSmart.length === 0"
        :title="t('systems.smartEmptyTitle')"
        :description="t('systems.smartEmptyDescription')"
      />
      <Card v-for="report in filteredSmart" :key="report.system_id" :padded="false">
        <div class="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
          <div>
            <RouterLink
              :to="{ name: 'system-detail', params: { id: report.system_id } }"
              class="text-sm font-semibold hover:underline"
            >
              {{ report.system_name }}
            </RouterLink>
            <p class="text-[11px] text-muted-foreground">
              {{ t('systems.smartCollected', { time: formatDateTime(report.collected_at, locale) }) }}
              <span v-if="!report.complete"> · {{ t('systems.smartPartial') }}</span>
            </p>
          </div>
          <Badge variant="secondary">{{ t('systems.disks', { count: Object.keys(report.disks ?? {}).length }) }}</Badge>
        </div>
        <div class="divide-y divide-border/60">
          <div
            v-for="(disk, device) in report.disks ?? {}"
            :key="device"
            class="flex flex-wrap items-center justify-between gap-3 px-4 py-2 text-xs"
          >
            <div class="min-w-0">
              <p class="font-medium">{{ disk.model_name || device }}</p>
              <p class="text-[11px] text-muted-foreground">
                {{ device }}<span v-if="disk.serial_number"> · {{ disk.serial_number }}</span>
                <span v-if="disk.capacity"> · {{ formatBytes(disk.capacity) }}</span>
              </p>
            </div>
            <div class="flex items-center gap-2">
              <span v-if="disk.temperature" class="text-[11px] text-muted-foreground">{{ disk.temperature }} °C</span>
              <Badge :variant="smartVariant(disk.status)">{{ disk.status || t('common.unknown') }}</Badge>
            </div>
          </div>
        </div>
      </Card>
    </section>

    <SystemDialog v-model="dialogOpen" :system="editing" @saved="loadOverview" />

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('systems.deleteTitle')"
      :description="t('systems.deleteDescription', { name: pendingRemoval?.name ?? '' })"
      :confirm-label="t('common.delete')"
      destructive
      @confirm="confirmRemoval"
    />

    <Dialog v-model="rotateOpen" :title="t('systems.rotateTitle')" :description="t('systems.rotateDescription')">
      <div class="space-y-3">
        <p class="text-xs text-muted-foreground">{{ t('systems.rotateHelp') }}</p>
        <div v-if="rotatedToken" class="flex items-start gap-2">
          <code class="flex-1 break-all rounded bg-muted px-2 py-1 font-mono text-[11px]">{{ rotatedToken }}</code>
          <Button variant="outline" size="icon" :aria-label="t('common.copy')" @click="copyRotated">
            <RotateCcw class="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </div>
        <p v-if="rotatedToken" class="text-[11px] text-destructive">{{ t('systems.tokenWarning') }}</p>
      </div>
      <template #footer>
        <Button variant="ghost" @click="rotateOpen = false">{{ t('common.close') }}</Button>
        <Button v-if="!rotatedToken" :loading="store.saving" @click="confirmRotate">
          <KeyRound class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('systems.rotateToken') }}
        </Button>
      </template>
    </Dialog>
  </div>
</template>
