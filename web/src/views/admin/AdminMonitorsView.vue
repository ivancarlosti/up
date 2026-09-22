<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { Pencil, Plus, RefreshCw, Trash2 } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import StatusBadge from '@/components/monitors/StatusBadge.vue'
import MonitorForm from '@/components/monitors/MonitorForm.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatInterval, formatUptime } from '@/lib/format'
import { useToastStore } from '@/stores/toast'
import type { Monitor, MonitorPayload, Notification } from '@/lib/types'

const { t } = useI18n()
const toasts = useToastStore()
const router = useRouter()

const monitors = ref<Monitor[]>([])
const notifications = ref<Notification[]>([])
const search = ref('')
const formOpen = ref(false)
const editing = ref<Monitor | null>(null)
const saving = ref(false)
const confirmOpen = ref(false)
const pendingRemoval = ref<Monitor | null>(null)

const filtered = computed(() => {
  const term = search.value.trim().toLowerCase()
  if (!term) return monitors.value
  return monitors.value.filter(
    (monitor) => monitor.name.toLowerCase().includes(term) || monitor.type.includes(term),
  )
})

async function load(): Promise<void> {
  try {
    const [list, channels] = await Promise.all([api.monitors(), api.notifications()])
    monitors.value = list
    notifications.value = channels
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
        <Input v-model="search" class="max-w-xs" :placeholder="t('dashboard.searchPlaceholder')" />
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

    <EmptyState v-if="!filtered.length" :title="t('dashboard.empty')" :description="t('dashboard.emptyHint')" />

    <Card v-else :padded="false">
      <table class="w-full text-xs">
        <thead class="text-left text-muted-foreground">
          <tr>
            <th class="px-4 py-3">{{ t('common.name') }}</th>
            <th class="px-4 py-3">{{ t('common.type') }}</th>
            <th class="px-4 py-3">{{ t('common.status') }}</th>
            <th class="px-4 py-3">{{ t('common.interval') }}</th>
            <th class="px-4 py-3">{{ t('common.uptime') }}</th>
            <th class="px-4 py-3">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="monitor in filtered" :key="monitor.id" class="border-t border-border">
            <td class="px-4 py-2.5">
              <button class="text-left hover:underline" @click="router.push({ name: 'monitor-detail', params: { id: String(monitor.id) } })">
                {{ monitor.name }}
              </button>
            </td>
            <td class="px-4 py-2.5"><Badge variant="secondary">{{ monitor.type }}</Badge></td>
            <td class="px-4 py-2.5"><StatusBadge :status="monitor.status" /></td>
            <td class="px-4 py-2.5">{{ formatInterval(monitor.interval_seconds) }}</td>
            <td class="px-4 py-2.5">{{ formatUptime(monitor.uptime_24h) }}</td>
            <td class="px-4 py-2.5">
              <div class="flex items-center gap-1">
                <Button variant="ghost" size="sm" @click="openEdit(monitor)">
                  <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
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

    <MonitorForm v-model="formOpen" :monitor="editing" :notifications="notifications" :saving="saving" @submit="submit" />

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('monitor.deleteTitle')"
      :description="t('monitor.deleteWarning')"
      :confirm-label="t('common.delete')"
      @confirm="confirmRemove"
    />
  </div>
</template>
