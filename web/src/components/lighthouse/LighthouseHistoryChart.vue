<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatDateTime, formatScore, scoreColor } from '@/lib/format'
import type { LighthouseRun } from '@/lib/types'

/**
 * LighthouseHistoryChart plots the performance score of every stored run.
 *
 * It is a CSS bar chart, like the heartbeat bars of the monitors table: the whole
 * application draws its trends with flex boxes and Tailwind tokens, so the chart
 * adds no dependency and follows the theme (and a failed run is drawn as a muted
 * column instead of a fake zero).
 */
const props = withDefaults(defineProps<{ runs: LighthouseRun[]; locale?: string }>(), {
  locale: 'en-US',
})

const { t } = useI18n()

const bars = computed(() =>
  props.runs.map((run) => ({
    key: run.id,
    run,
    // A failed run has no score: it is drawn as a discreet marker so the chart
    // never pretends the page scored zero.
    height: run.status === 'error' ? 4 : Math.max(2, Math.min(100, run.performance ?? 0)),
    color: run.status === 'error' ? 'bg-status-unknown/40' : scoreColor(run.performance),
    title:
      run.status === 'error'
        ? `${formatDateTime(run.ran_at, props.locale)} — ${run.message ?? t('lighthouse.runFailed')}`
        : `${formatDateTime(run.ran_at, props.locale)} — ${t('lighthouse.performance')} ${formatScore(run.performance)} · ` +
          `${t('lighthouse.accessibility')} ${formatScore(run.accessibility)} · ` +
          `${t('lighthouse.bestPractices')} ${formatScore(run.best_practices)} · ` +
          `${t('lighthouse.seo')} ${formatScore(run.seo)}`,
  })),
)

/** summary counts what the window holds, for the legend under the chart. */
const summary = computed(() => {
  const measured = props.runs.filter((run) => run.status !== 'error' && run.performance !== null)
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
        <span>{{ t('lighthouse.runsCount', { count: props.runs.length }) }}</span>
      </div>
    </template>
  </div>
</template>
