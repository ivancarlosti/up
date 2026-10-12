<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Save } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import {
  LIGHTHOUSE_INTERVALS,
  LIGHTHOUSE_INTERVAL_KEYS,
  LIGHTHOUSE_STRATEGIES,
  LIGHTHOUSE_WEEKDAYS,
  LIGHTHOUSE_WEEKDAY_KEYS,
  lighthouseInterval,
  lighthouseWeekday,
} from '@/lib/monitor-config'
import { useToastStore } from '@/stores/toast'
import type { LighthouseSettingsPayload } from '@/lib/types'

/**
 * LighthouseJobCard is the web performance job configuration (Admin > Settings,
 * reused by the Web performance page so the two views cannot drift).
 *
 * It owns its own load and save: the card is the single place where the schedule,
 * the pacing, the API key and the retention are edited.
 */
const emit = defineEmits<{ saved: [] }>()

const { t, locale } = useI18n()
const toast = useToastStore()

const loading = ref(false)
const saving = ref(false)
const nextRun = ref<string | null>(null)
const stats = ref<{ measuring: number; runs: number } | null>(null)
const retentionMax = ref(3650)
const retentionDefault = ref(30)

const form = ref<LighthouseSettingsPayload & { retention_days: number }>({
  enabled: true,
  check_time: '04:00',
  check_timezone: 'UTC',
  strategy: 'mobile',
  interval: 'daily',
  weekday: 'monday',
  rate_limit_ms: 60000,
  timeout_seconds: 90,
  api_key: '',
  retention_days: 30,
})

const strategyOptions = computed(() =>
  LIGHTHOUSE_STRATEGIES.map((strategy) => ({
    value: strategy,
    label: strategy === 'desktop' ? t('monitor.lighthouseStrategyDesktop') : t('monitor.lighthouseStrategyMobile'),
  })),
)

/** The cadences a monitor follows when it does not pick one of its own. */
const intervalOptions = computed(() =>
  LIGHTHOUSE_INTERVALS.map((interval) => ({ value: interval, label: t(LIGHTHOUSE_INTERVAL_KEYS[interval]) })),
)

/** The weekdays a weekly cadence can be anchored to. */
const weekdayOptions = computed(() =>
  LIGHTHOUSE_WEEKDAYS.map((weekday) => ({ value: weekday, label: t(LIGHTHOUSE_WEEKDAY_KEYS[weekday]) })),
)

/**
 * The time of day is picked with two explicit selects (00-23 and 00-59), exactly
 * like the expiry job: a native <input type="time"> renders AM/PM or 24h depending
 * on the browser locale, which cannot be overridden.
 */
const hourOptions = Array.from({ length: 24 }, (_, index) => {
  const value = String(index).padStart(2, '0')
  return { value, label: value }
})
const minuteOptions = Array.from({ length: 60 }, (_, index) => {
  const value = String(index).padStart(2, '0')
  return { value, label: value }
})
const checkHour = computed(() => form.value.check_time.split(':')[0] ?? '04')
const checkMinute = computed(() => form.value.check_time.split(':')[1] ?? '00')
function setCheckHour(value: string): void {
  form.value.check_time = `${value}:${checkMinute.value}`
}
function setCheckMinute(value: string): void {
  form.value.check_time = `${checkHour.value}:${value}`
}

/** String wrappers: the Select and the number inputs speak strings. */
const strategy = computed({
  get: () => String(form.value.strategy),
  set: (value: string) => {
    // The Select hands back a plain string; only the two runs exist server side
    // (mirror of `models.NormalizeLighthouseStrategy`).
    form.value.strategy = value === 'desktop' ? 'desktop' : 'mobile'
  },
})
const retentionDays = computed({
  get: () => String(form.value.retention_days),
  set: (value: string) => {
    form.value.retention_days = Number(value)
  },
})

/** The cadence of a monitor that does not carry one (Select contract). */
const interval = computed({
  get: () => String(form.value.interval),
  set: (value: string) => {
    form.value.interval = lighthouseInterval(value)
  },
})

/** The weekday the weekly cadence is anchored to (Select contract). */
const weekday = computed({
  get: () => String(form.value.weekday),
  set: (value: string) => {
    form.value.weekday = lighthouseWeekday(value)
  },
})

/** toNumber converts what a native number input produces (a string). */
function toNumber(value: unknown, fallback: number): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

async function load(): Promise<void> {
  loading.value = true
  try {
    const response = await api.lighthouseSettings()
    form.value = { ...response.settings, retention_days: response.retention_days }
    nextRun.value = response.next_run
    stats.value = response.stats
    retentionMax.value = response.retention_max
    retentionDefault.value = response.retention_default
  } catch (error) {
    toast.error(t('common.error'), translateError(error))
  } finally {
    loading.value = false
  }
}

async function save(): Promise<void> {
  saving.value = true
  try {
    await api.updateLighthouseSettings({
      ...form.value,
      rate_limit_ms: toNumber(form.value.rate_limit_ms, 60000),
      timeout_seconds: toNumber(form.value.timeout_seconds, 90),
      retention_days: toNumber(form.value.retention_days, retentionDefault.value),
    })
    toast.success(t('common.saved'))
    await load()
    emit('saved')
  } catch (error) {
    toast.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <Card :title="t('lighthouse.jobTitle')" :description="t('lighthouse.jobHelp')">
    <div class="grid gap-3 sm:grid-cols-2">
      <div class="grid gap-1 sm:col-span-2">
        <Switch v-model="form.enabled">{{ t('lighthouse.enabled') }}</Switch>
        <p class="text-[11px] text-muted-foreground">{{ t('lighthouse.enabledHelp') }}</p>
      </div>

      <div class="grid gap-1">
        <Label :help="t('lighthouse.checkTimeHelp')">{{ t('lighthouse.checkTime') }}</Label>
        <div class="flex items-center gap-2">
          <Select v-model="checkHour" :options="hourOptions" class="w-20" @update:model-value="setCheckHour" />
          <span class="text-muted-foreground">:</span>
          <Select v-model="checkMinute" :options="minuteOptions" class="w-20" @update:model-value="setCheckMinute" />
        </div>
      </div>

      <div class="grid gap-1">
        <Label for="lighthouse-timezone" :help="t('lighthouse.timezoneHelp')">{{ t('lighthouse.timezone') }}</Label>
        <Input id="lighthouse-timezone" v-model="form.check_timezone" placeholder="UTC" />
      </div>

      <div class="grid gap-1">
        <Label for="lighthouse-strategy" :help="t('lighthouse.strategyHelp')">{{ t('lighthouse.strategy') }}</Label>
        <Select id="lighthouse-strategy" v-model="strategy" :options="strategyOptions" />
      </div>

      <div class="grid gap-1">
        <Label for="lighthouse-interval" :help="t('lighthouse.intervalHelp')">
          {{ t('lighthouse.interval') }}
        </Label>
        <Select id="lighthouse-interval" v-model="interval" :options="intervalOptions" />
        <p class="text-[11px] text-muted-foreground">{{ t('lighthouse.intervalMonitorHelp') }}</p>
      </div>

      <div v-if="interval === 'weekly'" class="grid gap-1">
        <Label for="lighthouse-weekday" :help="t('lighthouse.weekdayHelp')">
          {{ t('lighthouse.weekday') }}
        </Label>
        <Select id="lighthouse-weekday" v-model="weekday" :options="weekdayOptions" />
      </div>

      <div class="grid gap-1">
        <Label for="lighthouse-rate-limit" :help="t('lighthouse.rateLimitHelp')">{{ t('lighthouse.rateLimit') }}</Label>
        <Input id="lighthouse-rate-limit" v-model="form.rate_limit_ms" type="number" min="0" max="3600000" />
      </div>

      <div class="grid gap-1">
        <Label for="lighthouse-timeout" :help="t('lighthouse.timeoutHelp')">{{ t('lighthouse.timeout') }}</Label>
        <Input id="lighthouse-timeout" v-model="form.timeout_seconds" type="number" min="5" max="300" />
      </div>

      <div class="grid gap-1">
        <Label for="lighthouse-api-key" :help="t('lighthouse.apiKeyHelp')">{{ t('lighthouse.apiKey') }}</Label>
        <Input id="lighthouse-api-key" v-model="form.api_key" type="password" autocomplete="off" placeholder="AIza..." />
      </div>

      <div class="grid gap-1">
        <Label for="lighthouse-retention" :help="t('lighthouse.retentionHelp', { days: retentionDefault })">
          {{ t('lighthouse.retention') }}
        </Label>
        <Input id="lighthouse-retention" v-model="retentionDays" type="number" min="0" :max="retentionMax" />
      </div>
    </div>

    <dl class="mt-4 grid gap-3 text-xs sm:grid-cols-3">
      <div>
        <dt class="text-muted-foreground">{{ t('lighthouse.nextRun') }}</dt>
        <dd class="mt-0.5">{{ nextRun ? formatDateTime(nextRun, locale) : '—' }}</dd>
      </div>
      <div>
        <dt class="text-muted-foreground">{{ t('lighthouse.measuring') }}</dt>
        <dd class="mt-0.5 tabular-nums">{{ stats?.measuring ?? 0 }}</dd>
      </div>
      <div>
        <dt class="text-muted-foreground">{{ t('lighthouse.storedRuns') }}</dt>
        <dd class="mt-0.5 tabular-nums">{{ stats?.runs ?? 0 }}</dd>
      </div>
    </dl>

    <Alert variant="info" class="mt-3">{{ t('lighthouse.quotaHelp') }}</Alert>

    <template #footer>
      <Button size="sm" :loading="saving || loading" @click="save">
        <Save class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('common.save') }}
      </Button>
    </template>
  </Card>
</template>
