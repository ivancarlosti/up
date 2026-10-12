<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { History, Play, RefreshCw, Trash2 } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import LighthouseHistoryChart from '@/components/lighthouse/LighthouseHistoryChart.vue'
import LighthouseScores from '@/components/lighthouse/LighthouseScores.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime, scoreColor } from '@/lib/format'
import {
  LIGHTHOUSE_INTERVAL_KEYS,
  effectiveLighthouseInterval,
  lighthouseResults,
  monitorTarget,
} from '@/lib/monitor-config'
import { useToastStore } from '@/stores/toast'
import type { LighthouseInterval, LighthouseRun, Monitor } from '@/lib/types'

/**
 * WebPerformanceView is the admin page of the Lighthouse measurements: the
 * monitors that measure their page with their last score, and the history of one
 * monitor.
 *
 * The job configuration (schedule, pacing, API key, retention) lives in
 * Admin > Settings, and the switch and the strategy belong to the monitor form
 * (and to a template), so this view never writes a monitor — it only asks for a
 * run.
 */
const { t, locale } = useI18n()
const toasts = useToastStore()

const loading = ref(false)
const running = ref(false)
const purging = ref(false)
const purgeOpen = ref(false)
const runningMonitor = ref<number | null>(null)

const monitors = ref<Monitor[]>([])
const nextRun = ref<string | null>(null)
const retentionDays = ref(30)
/** Cadence of a monitor that carries none: the backend applies it, the page shows it. */
const intervalDefault = ref<LighthouseInterval>('daily')

/**
 * How many runs the history dialog asks for. An hourly monitor stores 24 points a
 * day, so the chart reads the newest few hundred instead of the whole table.
 */
const HISTORY_LIMIT = 200

// History dialog: the runs of the monitor the operator opened.
const historyOpen = ref(false)
const historyLoading = ref(false)
const historyMonitor = ref<Monitor | null>(null)
const historyRuns = ref<LighthouseRun[]>([])

/** Text filter of the monitors table (name, target, strategy). */
const search = ref('')

/**
 * intervalOf resolves the cadence a monitor really measures with: its own, or the
 * job setting when it carries none (a monitor stored before the field existed).
 * It mirrors `lighthouse.IntervalFor` on the Go side, so the page and the ticker
 * can never disagree about when a page is measured.
 */
function intervalOf(monitor: Monitor): LighthouseInterval {
  return effectiveLighthouseInterval(monitor, intervalDefault.value)
}

/** intervalLabel translates a cadence (the stored value is a code like "6h"). */
function intervalLabel(monitor: Monitor): string {
  return t(LIGHTHOUSE_INTERVAL_KEYS[intervalOf(monitor)])
}

/**
 * nextRunLabel says when the monitor is measured again. A monitor that is past its
 * instant reads "due now" (the ticker picks it up within a minute), and one the
 * backend did not decorate reads "—" (it never ran, or the job is switched off).
 */
function nextRunLabel(monitor: Monitor): string {
  const next = monitor.lighthouse?.next_run
  if (!next) return '—'
  if (new Date(next).getTime() <= Date.now()) return t('lighthouse.dueNow')
  return t('lighthouse.dueAt', { when: formatDateTime(next, locale.value) })
}

/** filteredMonitors applies the text filter (name, target, strategy, cadence). */
const filteredMonitors = computed(() => {
  const term = search.value.trim().toLowerCase()
  if (!term) return monitors.value
  return monitors.value.filter((monitor) =>
    [monitor.name, monitorTarget(monitor), monitor.config?.lighthouse_strategy ?? '', intervalLabel(monitor)]
      .some((value) => value.toLowerCase().includes(term)),
  )
})

async function load(): Promise<void> {
  loading.value = true
  try {
    const response = await api.lighthouseMonitors()
    monitors.value = response.monitors
    nextRun.value = response.next_run
    intervalDefault.value = response.interval_default
    retentionDays.value = response.retention_days
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    loading.value = false
  }
}

/** runNow measures every deduplicated target (the job, without waiting a day). */
async function runNow(): Promise<void> {
  running.value = true
  try {
    await api.runLighthouseNow()
    toasts.success(t('lighthouse.runStarted'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    running.value = false
  }
}

/** runMonitor measures one monitor now and refreshes its row. */
async function runMonitor(monitor: Monitor): Promise<void> {
  runningMonitor.value = monitor.id
  try {
    await api.runLighthouseMonitor(monitor.id)
    toasts.success(t('lighthouse.runStarted'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    runningMonitor.value = null
  }
}

/** openHistory loads the runs of one monitor and opens the chart. */
async function openHistory(monitor: Monitor): Promise<void> {
  historyMonitor.value = monitor
  historyRuns.value = []
  historyOpen.value = true
  historyLoading.value = true
  try {
    const response = await api.lighthouseHistory(monitor.id, HISTORY_LIMIT)
    historyRuns.value = response.runs
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    historyLoading.value = false
  }
}

/** purge deletes the runs older than the retention policy. */
async function purge(): Promise<void> {
  purging.value = true
  try {
    const result = await api.purgeLighthouse()
    toasts.success(t('lighthouse.purged', { count: result.deleted }))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    purging.value = false
    purgeOpen.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="grid gap-4">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold">{{ t('lighthouse.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('lighthouse.subtitle', { days: retentionDays }) }}</p>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" :loading="loading" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button size="sm" :loading="running" @click="runNow">
          <Play class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('lighthouse.runNow') }}
        </Button>
        <Button variant="outline" size="sm" @click="purgeOpen = true">
          <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('lighthouse.purgeNow') }}
        </Button>
      </div>
    </div>

    <Card :title="t('lighthouse.monitorsTitle')" :description="t('lighthouse.monitorsHelp')">
      <div class="mb-3 flex flex-wrap items-end justify-between gap-3">
        <div class="grid gap-1 sm:max-w-xs">
          <Label for="lighthouse-search">{{ t('common.search') }}</Label>
          <Input id="lighthouse-search" v-model="search" :placeholder="t('lighthouse.searchPlaceholder')" />
        </div>
        <p class="text-xs text-muted-foreground">
          {{ t('lighthouse.nextRunDefault') }}: {{ nextRun ? formatDateTime(nextRun, locale) : '—' }}
        </p>
      </div>

      <EmptyState
        v-if="!filteredMonitors.length"
        :title="t('lighthouse.emptyTitle')"
        :description="t('lighthouse.emptyHelp')"
      />

      <div v-else class="overflow-x-auto">
        <table class="w-full text-sm">
          <thead>
            <tr class="text-xs text-muted-foreground">
              <th class="py-2 text-start font-medium">{{ t('common.name') }}</th>
              <th class="py-2 text-start font-medium">{{ t('lighthouse.target') }}</th>
              <th class="py-2 text-start font-medium">{{ t('lighthouse.scores') }}</th>
              <th class="py-2 text-start font-medium">{{ t('lighthouse.cadence') }}</th>
              <th class="py-2 text-start font-medium">{{ t('lighthouse.lastRun') }}</th>
              <th class="py-2 text-end font-medium">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="monitor in filteredMonitors" :key="monitor.id" class="border-t border-border align-top">
              <td class="py-2">
                <RouterLink :to="`/monitors/${monitor.id}`" class="text-sm font-medium hover:underline">
                  {{ monitor.name }}
                </RouterLink>
                <span class="block text-[11px] text-muted-foreground">
                  {{ monitor.type }}<span v-if="!monitor.active"> · {{ t('common.paused') }}</span>
                </span>
              </td>
              <td class="py-2">
                <span class="font-mono text-xs">{{ monitorTarget(monitor) }}</span>
                <span
                  v-if="monitor.lighthouse?.final_url && monitor.lighthouse.final_url !== monitorTarget(monitor)"
                  class="block font-mono text-[11px] text-muted-foreground"
                >
                  → {{ monitor.lighthouse.final_url }}
                </span>
              </td>
              <td class="py-2 sm:min-w-72">
                <div class="grid gap-2">
                  <!-- One block per run the monitor measures (two for "both"). -->
                  <LighthouseScores
                    v-for="result in lighthouseResults(monitor)"
                    :key="result.strategy"
                    :result="result"
                    :locale="locale"
                  />
                  <p v-if="!lighthouseResults(monitor).length" class="text-xs text-muted-foreground">
                    {{ t('lighthouse.neverMeasured') }}
                  </p>
                </div>
              </td>
              <td class="whitespace-nowrap py-2 text-xs">
                <span>{{ intervalLabel(monitor) }}</span>
                <span class="block text-[11px] text-muted-foreground">{{ nextRunLabel(monitor) }}</span>
              </td>
              <td class="whitespace-nowrap py-2 text-xs">
                <span v-if="monitor.lighthouse" class="flex items-center gap-2">
                  <span class="h-2 w-2 rounded-full" :class="scoreColor(monitor.lighthouse.performance)" aria-hidden="true" />
                  {{ formatDateTime(monitor.lighthouse.ran_at, locale) }}
                </span>
                <span v-else class="text-muted-foreground">{{ t('lighthouse.neverMeasured') }}</span>
              </td>
              <td class="py-2">
                <div class="flex justify-end gap-2">
                  <Button
                    variant="outline"
                    size="sm"
                    :loading="runningMonitor === monitor.id"
                    @click="runMonitor(monitor)"
                  >
                    <Play class="h-3.5 w-3.5" aria-hidden="true" />
                    {{ t('lighthouse.runNow') }}
                  </Button>
                  <Button variant="outline" size="sm" @click="openHistory(monitor)">
                    <History class="h-3.5 w-3.5" aria-hidden="true" />
                    {{ t('lighthouse.history') }}
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </Card>

    <Dialog
      v-model="historyOpen"
      :title="historyMonitor ? t('lighthouse.historyTitleOf', { name: historyMonitor.name }) : t('lighthouse.history')"
    >
      <div v-if="historyLoading" class="text-xs text-muted-foreground">{{ t('common.loading') }}</div>
      <div v-else class="grid gap-3">
        <LighthouseHistoryChart :runs="historyRuns" :locale="locale" />
        <div v-if="historyMonitor" class="grid gap-2">
          <LighthouseScores
            v-for="result in lighthouseResults(historyMonitor)"
            :key="result.strategy"
            :result="result"
            :locale="locale"
          />
        </div>
      </div>
      <template #footer>
        <Button variant="outline" @click="historyOpen = false">{{ t('common.close') }}</Button>
      </template>
    </Dialog>

    <ConfirmDialog
      v-model="purgeOpen"
      :title="t('lighthouse.purgeTitle')"
      :description="t('lighthouse.purgeWarning', { days: retentionDays })"
      :confirm-label="t('lighthouse.purgeNow')"
      :loading="purging"
      @confirm="purge"
    />
  </div>
</template>
