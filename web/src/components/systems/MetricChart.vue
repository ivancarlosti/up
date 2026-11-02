<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { formatBytes, formatPercent, formatRate } from '@/lib/format'

/**
 * MetricChart draws one hardware series as an inline SVG line.
 *
 * No charting dependency: the value is a single series of at most a few hundred
 * points, which is a polyline and a filled area. Theme colours come from Tailwind
 * tokens through `text-*` (`stroke-current` / `fill-current`), so the chart follows
 * the light and dark themes without a second stylesheet, like the rest of the UI.
 *
 * The series is drawn against its own minimum and maximum (with 0 always included
 * for percentages): an idle machine and a busy one both show their shape, and the
 * numbers under the chart are the ones the line was drawn from.
 */
const props = withDefaults(
  defineProps<{
    /** The series, in time order. */
    values: number[]
    /** Unit of the values. */
    unit?: 'percent' | 'bytes' | 'rate' | 'number'
    /** Tailwind text colour class of the line (defaults to the primary tone). */
    tone?: string
    /** Fixed maximum (percentages are always drawn against 100). */
    max?: number
    /** Height of the drawing area in pixels. */
    height?: number
    /** Rounds the y axis to the data instead of starting from 0 (rates, load). */
    autoScale?: boolean
  }>(),
  { unit: 'percent', tone: 'text-primary', max: undefined, height: 120, autoScale: false },
)

const { t } = useI18n()

const width = 640

/** bounds is the value range the line is drawn in. */
const bounds = computed(() => {
  const cap = props.max ?? (props.unit === 'percent' ? 100 : undefined)
  const values = props.values.filter((value) => Number.isFinite(value))
  const top = values.length ? Math.max(...values) : 0
  const bottom = values.length ? Math.min(...values) : 0
  const upper = cap ?? (props.autoScale ? top * 1.15 || 1 : Math.max(top, 1))
  const lower = props.autoScale && cap === undefined ? Math.min(bottom, upper) * 0.9 : 0
  return { upper: upper <= lower ? lower + 1 : upper, lower }
})

/** points is the geometry of the line: one x per value, y scaled to the bounds. */
const geometry = computed(() => {
  const values = props.values
  if (values.length === 0) return { line: '', area: '', points: [] as { x: number; y: number }[] }
  const { upper, lower } = bounds.value
  const span = upper - lower || 1
  const step = values.length > 1 ? width / (values.length - 1) : width
  const points = values.map((value, index) => {
    const clamped = Math.min(Math.max(value, lower), upper)
    const y = props.height - ((clamped - lower) / span) * props.height
    return { x: Number((index * step).toFixed(2)), y: Number(y.toFixed(2)) }
  })
  const line = points.map((point) => `${point.x},${point.y}`).join(' ')
  const area = `${points[0].x},${props.height} ${line} ${points[points.length - 1].x},${props.height}`
  return { line, area, points }
})

/** format renders a value with the unit of the chart. */
function format(value: number | null | undefined): string {
  if (value === null || value === undefined || Number.isNaN(value)) return '—'
  switch (props.unit) {
    case 'bytes':
      return formatBytes(value)
    case 'rate':
      return formatRate(value)
    case 'percent':
      return formatPercent(value)
    default:
      return String(Number(value.toFixed(2)))
  }
}

/** stats are the numbers the legend under the chart shows. */
const stats = computed(() => {
  const values = props.values.filter((value) => Number.isFinite(value))
  if (values.length === 0) return null
  const sum = values.reduce((total, value) => total + value, 0)
  return {
    min: Math.min(...values),
    max: Math.max(...values),
    avg: sum / values.length,
    last: values[values.length - 1],
  }
})

/** gridlines are the horizontal guides (a quarter of the scale each). */
const gridlines = computed(() => [0.25, 0.5, 0.75].map((ratio) => props.height * ratio))
</script>

<template>
  <div :class="['w-full', props.tone]">
    <svg
      v-if="geometry.points.length"
      :viewBox="`0 0 ${width} ${props.height}`"
      :style="{ height: `${props.height}px` }"
      class="w-full"
      preserveAspectRatio="none"
      role="img"
      :aria-label="t('systems.chartAria')"
    >
      <line
        v-for="(y, index) in gridlines"
        :key="index"
        :x1="0"
        :x2="width"
        :y1="y"
        :y2="y"
        class="stroke-border"
        stroke-width="1"
        stroke-dasharray="4 6"
        vector-effect="non-scaling-stroke"
      />
      <polygon :points="geometry.area" class="fill-current opacity-10" />
      <polyline
        :points="geometry.line"
        fill="none"
        stroke="currentColor"
        stroke-width="1.5"
        stroke-linejoin="round"
        stroke-linecap="round"
        vector-effect="non-scaling-stroke"
      />
    </svg>
    <div
      v-else
      class="flex items-center justify-center rounded-md border border-dashed border-border text-xs text-muted-foreground"
      :style="{ height: `${props.height}px` }"
    >
      {{ t('systems.noData') }}
    </div>

    <dl v-if="stats" class="mt-2 grid grid-cols-4 gap-2 text-[11px] text-muted-foreground">
      <div>
        <dt>{{ t('systems.chartLast') }}</dt>
        <dd class="font-medium text-foreground">{{ format(stats.last) }}</dd>
      </div>
      <div>
        <dt>{{ t('systems.chartAvg') }}</dt>
        <dd class="font-medium text-foreground">{{ format(stats.avg) }}</dd>
      </div>
      <div>
        <dt>{{ t('systems.chartMin') }}</dt>
        <dd class="font-medium text-foreground">{{ format(stats.min) }}</dd>
      </div>
      <div>
        <dt>{{ t('systems.chartMax') }}</dt>
        <dd class="font-medium text-foreground">{{ format(stats.max) }}</dd>
      </div>
    </dl>
  </div>
</template>
