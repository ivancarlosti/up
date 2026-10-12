<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { ArrowLeft, ExternalLink, Play, RefreshCw } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import LighthouseHistoryChart from '@/components/lighthouse/LighthouseHistoryChart.vue'
import LighthouseScores from '@/components/lighthouse/LighthouseScores.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime, formatScore, scoreTextColor } from '@/lib/format'
import {
  LIGHTHOUSE_INTERVAL_KEYS,
  LIGHTHOUSE_STRATEGY_KEYS,
  effectiveLighthouseInterval,
  lighthouseResults,
  lighthouseStrategy,
  monitorTarget,
  monitorTargetURL,
} from '@/lib/monitor-config'
import { useToastStore } from '@/stores/toast'
import type { LighthouseInterval, LighthouseResult, LighthouseRun, Monitor } from '@/lib/types'

/**
 * WebPerformanceDetailView is the results page of ONE measured page: the last
 * observation of every run the monitor measures (two of them for `both`), with
 * the details the listing has no room for (the vitals, the audited URL, the node
 * that ran it) and the whole stored history.
 *
 * The listing keeps the scores and links here; the history *dialog* of the
 * listing is untouched, because it answers a different question ("how did this
 * page move lately?") without leaving the table.
 *
 * The page never writes a monitor: measuring on demand is the job's own endpoint,
 * and the switch, the strategy and the cadence belong to the monitor form.
 */
const route = useRoute()
const toasts = useToastStore()
const { t, locale } = useI18n()

/** How many runs the page reads: the newest few hundred, like the list dialog. */
const HISTORY_LIMIT = 200

const monitorId = computed(() => Number(route.params.id))
const loading = ref(false)
const running = ref(false)
/** The message of a load that failed (a monitor that does not exist, a node down). */
const failure = ref('')

const monitor = ref<Monitor | null>(null)
/** The row of the listing: the decorated one, with the stored scores and the clock. */
const listed = ref<Monitor | null>(null)
const runs = ref<LighthouseRun[]>([])
/** Cadence of a monitor that carries none: the backend applies it, the page shows it. */
const intervalDefault = ref<LighthouseInterval>('weekly')
const retentionDays = ref(30)

/** subject is the monitor the page renders, seen through the listing when it has it. */
const subject = computed<Monitor | null>(() => listed.value ?? monitor.value)

/**
 * results are the last observation of each run the monitor measures with: one
 * entry for a single run, two for `both` (mobile first). They come from the
 * listing row, which is the surface the backend decorates with `lighthouses`.
 */
const results = computed<LighthouseResult[]>(() => (subject.value ? lighthouseResults(subject.value) : []))

/** strategyLabel names how the monitor is measured (mobile, desktop, both). */
const strategyLabel = computed(() =>
  subject.value ? t(LIGHTHOUSE_STRATEGY_KEYS[lighthouseStrategy(subject.value.config?.lighthouse_strategy)]) : '',
)

/** cadenceLabel is the cadence the monitor really measures with, fallback included. */
const cadenceLabel = computed(() =>
  subject.value ? t(LIGHTHOUSE_INTERVAL_KEYS[effectiveLighthouseInterval(subject.value, intervalDefault.value)]) : '',
)

/** targetURL is the target when it is a page that can be opened, so the header links it. */
const targetURL = computed(() => (subject.value ? monitorTargetURL(subject.value) : null))

/** history lists the runs newest first: the point the page opens on. */
const history = computed(() => [...runs.value].reverse())

/** runStrategyLabel names the run of a history row (a stored run is never "both"). */
function runStrategyLabel(run: LighthouseRun): string {
  return t(LIGHTHOUSE_STRATEGY_KEYS[run.strategy])
}

/**
 * facts are the details of an observation the score chips do not carry: the page
 * Lighthouse really audited (after the redirects), when Lighthouse rendered it and
 * which node called the API. An absent value is left out instead of rendered as a
 * dash, because an observation stored by an older node may simply not have it.
 */
function facts(result: LighthouseResult): { label: string; value: string; href?: string }[] {
  const out: { label: string; value: string; href?: string }[] = []
  if (result.final_url) {
    out.push({ label: t('lighthouse.finalUrl'), value: result.final_url, href: result.final_url })
  }
  if (result.fetch_time) {
    out.push({ label: t('lighthouse.fetchTime'), value: formatDateTime(result.fetch_time, locale.value) })
  }
  if (result.ran_by_node) out.push({ label: t('lighthouse.node'), value: result.ran_by_node })
  return out
}

async function load(): Promise<void> {
  if (!Number.isInteger(monitorId.value) || monitorId.value <= 0) {
    failure.value = t('errors.ERR_MONITOR_NOT_FOUND')
    return
  }
  loading.value = true
  failure.value = ''
  try {
    const [detail, historyResponse, listing] = await Promise.all([
      api.monitor(monitorId.value),
      api.lighthouseHistory(monitorId.value, HISTORY_LIMIT),
      api.lighthouseMonitors(),
    ])
    monitor.value = detail
    runs.value = historyResponse.runs
    listed.value = listing.monitors.find((row) => row.id === monitorId.value) ?? null
    intervalDefault.value = listing.interval_default
    retentionDays.value = listing.retention_days
  } catch (error) {
    failure.value = translateError(error)
    toasts.error(t('common.error'), failure.value)
  } finally {
    loading.value = false
  }
}

/** runNow measures the page again without waiting for the schedule. */
async function runNow(): Promise<void> {
  running.value = true
  try {
    await api.runLighthouseMonitor(monitorId.value)
    toasts.success(t('lighthouse.runStarted'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    running.value = false
  }
}

onMounted(load)
// A link from the listing of another monitor reuses this route: without the watch
// the page would keep rendering the previous one.
watch(monitorId, load)
</script>

<template>
  <div class="grid gap-4">
    <div class="flex flex-wrap items-start justify-between gap-3">
      <div class="min-w-0">
        <RouterLink
          :to="{ name: 'admin-web-performance' }"
          class="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground hover:underline"
        >
          <ArrowLeft class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('lighthouse.backToList') }}
        </RouterLink>
        <h1 class="mt-1 truncate text-lg font-semibold">{{ subject?.name ?? t('lighthouse.title') }}</h1>
        <a
          v-if="targetURL"
          class="block truncate font-mono text-xs text-muted-foreground hover:text-foreground hover:underline"
          :href="targetURL"
          target="_blank"
          rel="noopener noreferrer"
        >
          {{ monitorTarget(subject as Monitor) }}
        </a>
        <p v-else-if="subject" class="truncate font-mono text-xs text-muted-foreground">
          {{ monitorTarget(subject) }}
        </p>
        <div v-if="subject" class="mt-1.5 flex flex-wrap items-center gap-1.5">
          <Badge variant="secondary">{{ subject.type }}</Badge>
          <Badge variant="outline">{{ strategyLabel }}</Badge>
          <Badge variant="outline">{{ cadenceLabel }}</Badge>
        </div>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <Button variant="outline" size="sm" :loading="loading" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button size="sm" :disabled="!subject" :loading="running" @click="runNow">
          <Play class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('lighthouse.runNow') }}
        </Button>
        <RouterLink v-if="subject" :to="{ name: 'monitor-detail', params: { id: subject.id } }">
          <Button variant="outline" size="sm">
            <ExternalLink class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('lighthouse.openMonitor') }}
          </Button>
        </RouterLink>
      </div>
    </div>

    <EmptyState v-if="failure" :title="t('common.error')" :description="failure">
      <RouterLink :to="{ name: 'admin-web-performance' }">
        <Button variant="outline" size="sm">{{ t('lighthouse.backToList') }}</Button>
      </RouterLink>
    </EmptyState>

    <template v-else>
      <Card :title="t('lighthouse.results')">
        <EmptyState
          v-if="!results.length"
          :title="t('lighthouse.emptyTitle')"
          :description="subject?.lighthouse_enabled ? t('lighthouse.neverMeasured') : t('lighthouse.emptyHelp')"
        />
        <!-- One block per run the monitor measures: two of them for "both". -->
        <div v-else class="grid gap-3" :class="results.length > 1 ? 'lg:grid-cols-2' : ''">
          <div
            v-for="result in results"
            :key="result.strategy"
            class="grid gap-2 rounded-lg border border-border p-3"
          >
            <LighthouseScores :result="result" :locale="locale" />
            <dl v-if="facts(result).length" class="grid gap-1 border-t border-border pt-2 text-[11px]">
              <div v-for="fact in facts(result)" :key="fact.label" class="flex flex-wrap items-baseline gap-x-2">
                <dt class="text-muted-foreground">{{ fact.label }}</dt>
                <dd class="min-w-0 break-all">
                  <a
                    v-if="fact.href"
                    class="font-mono hover:underline"
                    :href="fact.href"
                    target="_blank"
                    rel="noopener noreferrer"
                  >
                    {{ fact.value }}
                  </a>
                  <span v-else class="font-mono">{{ fact.value }}</span>
                </dd>
              </div>
            </dl>
          </div>
        </div>
      </Card>

      <!--
        The whole stored history of the page: the same chart the listing opens in
        a dialog, plus the numbers of every run. A run measured on "both" stores
        one row per run, which is what the strategy badge and the chart selector
        tell apart.
      -->
      <Card :title="t('lighthouse.runHistory')" :description="t('lighthouse.retentionNote', { days: retentionDays })">
        <div class="grid gap-3">
          <LighthouseHistoryChart :runs="runs" :locale="locale" />

          <div v-if="history.length" class="overflow-x-auto">
            <table class="w-full text-sm">
              <thead>
                <tr class="text-xs text-muted-foreground">
                  <th class="py-2 text-start font-medium">{{ t('lighthouse.runAt') }}</th>
                  <th class="py-2 text-start font-medium">{{ t('lighthouse.strategy') }}</th>
                  <th class="py-2 text-end font-medium">{{ t('lighthouse.performance') }}</th>
                  <th class="py-2 text-end font-medium">{{ t('lighthouse.accessibility') }}</th>
                  <th class="py-2 text-end font-medium">{{ t('lighthouse.bestPractices') }}</th>
                  <th class="py-2 text-end font-medium">{{ t('lighthouse.seo') }}</th>
                  <th class="py-2 ps-3 text-start font-medium">{{ t('lighthouse.node') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="run in history" :key="run.id" class="border-t border-border">
                  <td class="whitespace-nowrap py-2 text-xs">{{ formatDateTime(run.ran_at, locale) }}</td>
                  <td class="py-2">
                    <Badge variant="secondary">{{ runStrategyLabel(run) }}</Badge>
                    <!-- A failed run has no scores: the four columns stay dashed. -->
                    <Badge v-if="run.status === 'error'" variant="danger" class="ms-1" :title="run.message">
                      {{ t('lighthouse.runFailed') }}
                    </Badge>
                  </td>
                  <td class="py-2 text-end tabular-nums" :class="scoreTextColor(run.performance)">
                    {{ formatScore(run.performance) }}
                  </td>
                  <td class="py-2 text-end tabular-nums" :class="scoreTextColor(run.accessibility)">
                    {{ formatScore(run.accessibility) }}
                  </td>
                  <td class="py-2 text-end tabular-nums" :class="scoreTextColor(run.best_practices)">
                    {{ formatScore(run.best_practices) }}
                  </td>
                  <td class="py-2 text-end tabular-nums" :class="scoreTextColor(run.seo)">
                    {{ formatScore(run.seo) }}
                  </td>
                  <td class="py-2 ps-3 text-xs text-muted-foreground">{{ run.ran_by_node || '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </Card>
    </template>
  </div>
</template>
