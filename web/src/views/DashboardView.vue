<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { Plus, Search } from 'lucide-vue-next'
import { useI18n } from 'vue-i18n'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Select from '@/components/ui/Select.vue'
import MonitorCard from '@/components/monitors/MonitorCard.vue'
import MonitorForm from '@/components/monitors/MonitorForm.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatRelative } from '@/lib/format'
import { useMonitorStore } from '@/stores/monitors'
import { useToastStore } from '@/stores/toast'
import type { Monitor, MonitorPayload, Notification } from '@/lib/types'

const monitors = useMonitorStore()
const toasts = useToastStore()
const router = useRouter()
const { t, locale } = useI18n()

const notifications = ref<Notification[]>([])
const formOpen = ref(false)
const editing = ref<Monitor | null>(null)
const saving = ref(false)
const confirmOpen = ref(false)
const pendingRemoval = ref<Monitor | null>(null)
const removing = ref(false)
const search = ref('')
const typeFilter = ref('')

const filtered = computed(() => {
  const term = search.value.trim().toLowerCase()
  return monitors.sorted.filter((monitor) => {
    if (typeFilter.value && monitor.type !== typeFilter.value) return false
    if (!term) return true
    return (
      monitor.name.toLowerCase().includes(term) ||
      monitor.description?.toLowerCase().includes(term) ||
      monitor.tags?.toLowerCase().includes(term)
    )
  })
})

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
  await Promise.all([load(), loadNotifications()])
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
          <Search class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('dashboard.addMonitor') }}
        </Button>
      </div>
    </header>

    <dl class="grid grid-cols-2 gap-4 sm:grid-cols-3 lg:grid-cols-6">
      <div class="rounded-xl border border-border bg-card px-4 py-3.5">
        <dt class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ t('dashboard.statTotal') }}</dt>
        <dd class="mt-2 text-xl font-semibold leading-none tabular-nums">{{ monitors.summary?.total ?? 0 }}</dd>
      </div>
      <div class="rounded-xl border border-border bg-card px-4 py-3.5">
        <dt class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ t('dashboard.statUp') }}</dt>
        <dd class="mt-2 text-xl font-semibold leading-none tabular-nums text-status-up">{{ monitors.summary?.up ?? 0 }}</dd>
      </div>
      <div class="rounded-xl border border-border bg-card px-4 py-3.5">
        <dt class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ t('dashboard.statDown') }}</dt>
        <dd class="mt-2 text-xl font-semibold leading-none tabular-nums text-status-down">{{ monitors.summary?.down ?? 0 }}</dd>
      </div>
      <div class="rounded-xl border border-border bg-card px-4 py-3.5">
        <dt class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ t('dashboard.statDegraded') }}</dt>
        <dd class="mt-2 text-xl font-semibold leading-none tabular-nums text-status-degraded">{{ monitors.summary?.degraded ?? 0 }}</dd>
      </div>
      <div class="rounded-xl border border-border bg-card px-4 py-3.5">
        <dt class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ t('dashboard.statHeartbeats') }}</dt>
        <dd class="mt-2 text-xl font-semibold leading-none tabular-nums">{{ monitors.summary?.heartbeats_1h ?? 0 }}</dd>
      </div>
      <div class="rounded-xl border border-border bg-card px-4 py-3.5">
        <dt class="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{{ t('dashboard.statClients') }}</dt>
        <dd class="mt-2 text-xl font-semibold leading-none tabular-nums">{{ monitors.summary?.ws_clients ?? 0 }}</dd>
      </div>
    </dl>

    <Alert v-if="degradedNodes.length" variant="warning">
      {{ t('dashboard.degradedWarning') }} ({{ degradedNodes.join(', ') }})
    </Alert>

    <div class="flex flex-wrap items-center gap-2">
      <Input v-model="search" class="max-w-xs" :placeholder="t('dashboard.searchPlaceholder')" />
      <Select v-model="typeFilter" :options="typeOptions" class="max-w-[12rem]" />
      <span class="ms-auto text-[11px] text-muted-foreground">
        {{ t('common.lastCheck') }}: {{ formatRelative(monitors.summary ? new Date().toISOString() : null, locale) }}
      </span>
    </div>

    <EmptyState v-if="!monitors.loading && filtered.length === 0" :title="t('dashboard.empty')" :description="t('dashboard.emptyHint')">
      <Button size="sm" @click="openCreate">
        <Plus class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('dashboard.addMonitor') }}
      </Button>
    </EmptyState>

    <div v-else class="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
      <MonitorCard
        v-for="monitor in filtered"
        :key="monitor.id"
        :monitor="monitor"
        @open="openDetail"
        @edit="openEdit"
        @toggle="toggle"
        @check="checkNow"
        @remove="askRemove"
      />
    </div>

    <MonitorForm
      v-model="formOpen"
      :monitor="editing"
      :notifications="notifications"
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

