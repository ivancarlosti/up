<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Badge from '@/components/ui/Badge.vue'
import { formatDateTime, formatScore, formatVital, scoreColor, scoreTextColor } from '@/lib/format'
import type { LighthouseResult } from '@/lib/types'

/**
 * LighthouseScores shows the last web performance observation of a monitor: the
 * four category scores as coloured chips plus the Core Web Vitals.
 *
 * A category the API could not measure is rendered as a dash (never as zero), and
 * a failed run (an unreachable page, a refused API call) is rendered as an error
 * badge whose tooltip carries the API message. `compact` keeps only the scores,
 * which is what the monitors table needs.
 */
const props = withDefaults(
  defineProps<{ result?: LighthouseResult | null; compact?: boolean; locale?: string }>(),
  { result: null, compact: false, locale: 'en-US' },
)

const { t } = useI18n()

/** The four categories, in the order the Lighthouse report lists them. */
const categories = computed(() => [
  { key: 'performance', label: t('lighthouse.performance'), score: props.result?.performance ?? null },
  { key: 'accessibility', label: t('lighthouse.accessibility'), score: props.result?.accessibility ?? null },
  { key: 'best-practices', label: t('lighthouse.bestPractices'), score: props.result?.best_practices ?? null },
  { key: 'seo', label: t('lighthouse.seo'), score: props.result?.seo ?? null },
])

const failed = computed(() => props.result?.status === 'error')

/** The vitals, in the order the page reads them. */
const vitals = computed(() => {
  const result = props.result
  if (!result) return []
  return [
    { key: 'fcp', label: t('lighthouse.fcp'), value: formatVital(result.first_contentful_paint_ms) },
    { key: 'lcp', label: t('lighthouse.lcp'), value: formatVital(result.largest_contentful_paint_ms) },
    { key: 'tbt', label: t('lighthouse.tbt'), value: formatVital(result.total_blocking_time_ms) },
    { key: 'cls', label: t('lighthouse.cls'), value: formatVital(result.cumulative_layout_shift, 'cls') },
    { key: 'si', label: t('lighthouse.speedIndex'), value: formatVital(result.speed_index_ms) },
  ]
})

const strategyLabel = computed(() =>
  props.result?.strategy === 'desktop' ? t('monitor.lighthouseStrategyDesktop') : t('monitor.lighthouseStrategyMobile'),
)
</script>

<template>
  <div class="grid gap-2">
    <div v-if="!props.result" class="text-xs text-muted-foreground">{{ t('lighthouse.neverMeasured') }}</div>

    <template v-else>
      <div class="flex flex-wrap items-center gap-2">
        <Badge v-if="failed" variant="outline" :title="props.result.message">
          {{ t('lighthouse.runFailed') }}
        </Badge>
        <template v-else>
          <span
            v-for="category in categories"
            :key="category.key"
            class="inline-flex items-center gap-1.5 rounded-md border border-border px-2 py-1 text-xs"
            :title="`${category.label}: ${formatScore(category.score)}`"
          >
            <span class="h-2 w-2 rounded-full" :class="scoreColor(category.score)" aria-hidden="true" />
            <span class="text-muted-foreground">{{ category.label }}</span>
            <span class="font-medium tabular-nums" :class="scoreTextColor(category.score)">
              {{ formatScore(category.score) }}
            </span>
          </span>
        </template>
        <Badge variant="secondary">{{ strategyLabel }}</Badge>
      </div>

      <div v-if="failed" class="text-xs text-status-down">{{ props.result.message }}</div>

      <div v-else-if="!props.compact" class="flex flex-wrap gap-x-4 gap-y-1 text-[11px] text-muted-foreground">
        <span v-for="vital in vitals" :key="vital.key">
          <span class="uppercase">{{ vital.label }}</span>
          <span class="ms-1 tabular-nums text-foreground">{{ vital.value }}</span>
        </span>
      </div>

      <div v-if="!props.compact" class="text-[11px] text-muted-foreground">
        {{ t('lighthouse.measuredAt', { when: formatDateTime(props.result.ran_at, props.locale) }) }}
      </div>
    </template>
  </div>
</template>
