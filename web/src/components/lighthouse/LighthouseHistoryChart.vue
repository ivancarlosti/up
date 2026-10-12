<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Select from '@/components/ui/Select.vue'
import { formatDateTime, formatScore, scoreColor } from '@/lib/format'
import { LIGHTHOUSE_STRATEGY_KEYS } from '@/lib/monitor-config'
import type { LighthouseRun, LighthouseRunStrategy } from '@/lib/types'

/**
 * LighthouseHistoryChart plots the performance score of every stored run.
 *
 * It is a CSS bar chart, like the heartbeat bars of the monitors table: the whole
 * application draws its trends with flex boxes and Tailwind tokens, so the chart
 * adds no dependency and follows the theme (and a failed run is drawn as a muted
 * column instead of a fake zero).
 *
 * A monitor measured on "both" stores two points per instant (mobile and
 * desktop). They are NEVER drawn interleaved: the window is read one run at a
 * time, with a selector that only appears when it holds more than one. The
 * tooltip and the legend follow the selection, so "runs" always means the runs
 * the chart is showing.
 */
const props = withDefaults(defineProps<{ runs: LighthouseRun[]; locale?: string }>(), {
  locale: 'en-US',
})

const { t } = useI18n()

/** The selection: one run, or the whole window (the default). */
const selected = ref<'all' | LighthouseRunStrategy>('all')

/** The runs the window holds, in the canonical order (mobile first). */
const strategies = computed<LighthouseRunStrategy[]>(() => {
  const seen = new Set<LighthouseRunStrategy>()
  for (const run of props.runs) if (run.strategy) seen.add(run.strategy)
  return (['mobile', 'desktop'] as const).filter((strategy) => seen.has(strategy))
})

const strategyOptions = computed(() => [
  { value: 'all', label: t('lighthouse.strategyAll') },
  ...strategies.value.map((strategy) => ({ value: strategy, label: t(LIGHTHOUSE_STRATEGY_KEYS[strategy]) })),
])

/** visible is what the chart draws: the whole window, or the selected run. */
const visible = computed(() =>
  selected.value === 'all' ? props.runs : props.runs.filter((run) => run.strategy === selected.value),
)

/** strategyOf names the run of a bar, for the tooltip (never inside a single run). */
function strategyOf(run: LighthouseRun): string {
  return t(LIGHTHOUSE_STRATEGY_KEYS[run.strategy] ?? LIGHTHOUSE_STRATEGY_KEYS.mobile)
}

const bars = computed(() =>
  visible.value.map((run) => {
    const label = selected.value === 'all' ? `${strategyOf(run)} · ` : ''
    return {
      key: run.id,
      run,
      // A failed run has no score: it is drawn as a discreet marker so the chart
      // never pretends the page scored zero.
      height: run.status === 'error' ? 4 : Math.max(2, Math.min(100, run.performance ?? 0)),
      color: run.status === 'error' ? 'bg-status-unknown/40' : scoreColor(run.performance),
      title:
        run.status === 'error'
          ? `${label}${formatDateTime(run.ran_at, props.locale)} — ${run.message ?? t('lighthouse.runFailed')}`
          : `${label}${formatDateTime(run.ran_at, props.locale)} — ${t('lighthouse.performance')} ${formatScore(run.performance)} · ` +
            `${t('lighthouse.accessibility')} ${formatScore(run.accessibility)} · ` +
            `${t('lighthouse.bestPractices')} ${formatScore(run.best_practices)} · ` +
            `${t('lighthouse.seo')} ${formatScore(run.seo)}`,
    }
  }),
)

/** summary counts what the chart is showing, for the legend under it. */
const summary = computed(() => {
  const measured = visible.value.filter((run) => run.status !== 'error' && run.performance !== null)
  if (!measured.length) return null
  const scores = measured.map((run) => run.performance as number)
  const latest = scores[scores.length - 1]
  const best = Math.max(...scores)
  const worst = Math.min(...scores)
  return { latest, best, worst }
})
</script>

<template>
  <div class="grid gap-2">
    <div v-if="!bars.length" class="text-xs text-muted-foreground">{{ t('lighthouse.noHistory') }}</div>

    <template v-else>
      <div v-if="strategies.length > 1" class="flex items-center justify-between gap-2">
        <span class="text-[11px] text-muted-foreground">{{ t('lighthouse.strategy') }}</span>
        <Select v-model="selected" :options="strategyOptions" class="h-8 w-44 text-xs" />
      </div>
      <div
        class="flex h-24 items-end gap-1 rounded-md border border-border bg-muted/20 p-2"
        role="img"
        :aria-label="t('lighthouse.historyTitle')"
      >
        <span
          v-for="bar in bars"
          :key="bar.key"
          class="flex-1 rounded-sm"
          :class="bar.color"
          :style="{ height: `${bar.height}%` }"
          :title="bar.title"
        />
      </div>
      <div v-if="summary" class="flex flex-wrap gap-x-4 text-[11px] text-muted-foreground">
        <span>{{ t('lighthouse.latest') }} <span class="tabular-nums text-foreground">{{ formatScore(summary.latest) }}</span></span>
        <span>{{ t('lighthouse.best') }} <span class="tabular-nums text-foreground">{{ formatScore(summary.best) }}</span></span>
        <span>{{ t('lighthouse.worst') }} <span class="tabular-nums text-foreground">{{ formatScore(summary.worst) }}</span></span>
        <span>{{ t('lighthouse.runsCount', { count: visible.length }) }}</span>
      </div>
    </template>
  </div>
</template>
