<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Save, Trash2 } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import LighthouseJobCard from '@/components/lighthouse/LighthouseJobCard.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { uptimeWindowOptions } from '@/lib/uptime-window'
import { useAppStore } from '@/stores/app'
import { useToastStore } from '@/stores/toast'
import type { AdminSettings, ExpirySettings, HeartbeatRetentionStats } from '@/lib/types'

const { t, locale } = useI18n()
const toast = useToastStore()
const app = useAppStore()

const settings = ref<AdminSettings | null>(null)
const retention = ref<HeartbeatRetentionStats | null>(null)
const form = ref({
  default_locale: 'en-US',
  default_theme: 'system',
  time_format: 'auto',
  app_name: '',
  heartbeat_retention_days: 180,
  uptime_window_hours: 24,
})
const saving = ref(false)
const purgeOpen = ref(false)
const purging = ref(false)

// --- Certificate & domain expiration (moved here from the TLD/SSL page) -----
// The job settings live with the other runtime settings; the TLD/SSL page keeps
// the target list and the WHOIS rules. The field names are unchanged, so the API
// payload is exactly the one the expiry endpoint always accepted.
const expiryForm = ref<ExpirySettings>({
  check_time: '03:00',
  check_timezone: 'UTC',
  rdap_enabled: true,
  whois_enabled: true,
  rate_limit_ms: 100,
  timeout_seconds: 10,
})
const expirySaved = ref(false)

/** Common IANA names offered as suggestions (the field accepts any name). */
const timezoneSuggestions = [
  'UTC',
  'America/Sao_Paulo',
  'America/New_York',
  'America/Los_Angeles',
  'Europe/London',
  'Europe/Lisbon',
  'Europe/Madrid',
  'Europe/Berlin',
  'Asia/Tokyo',
  'Australia/Sydney',
]

/**
 * The time of day is picked with two explicit selects (00-23 and 00-59): a native
 * <input type="time"> renders AM/PM or 24h depending on the browser locale, which
 * cannot be overridden, and the operator wants a predictable clock here.
 */
const hourOptions = Array.from({ length: 24 }, (_, index) => {
  const value = String(index).padStart(2, '0')
  return { value, label: value }
})
const minuteOptions = Array.from({ length: 60 }, (_, index) => {
  const value = String(index).padStart(2, '0')
  return { value, label: value }
})
const checkHour = computed(() => expiryForm.value.check_time.split(':')[0] ?? '03')
const checkMinute = computed(() => expiryForm.value.check_time.split(':')[1] ?? '00')
function setCheckHour(value: string): void {
  expiryForm.value.check_time = `${value}:${checkMinute.value}`
}
function setCheckMinute(value: string): void {
  expiryForm.value.check_time = `${checkHour.value}:${value}`
}

/** toNumber converts what a native number input produces (a string). */
function toNumber(value: unknown, fallback: number): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

const localeOptions = computed(() =>
  (settings.value?.supported_locales ?? []).map((value) => ({ value, label: value })),
)
const themeOptions = computed(() => [
  { value: 'system', label: t('settings.themeSystem') },
  { value: 'light', label: t('settings.themeLight') },
  { value: 'dark', label: t('settings.themeDark') },
])
const timeFormatLabels: Record<string, string> = {
  auto: 'settings.timeFormatAuto',
  '12h': 'settings.timeFormat12h',
  '24h': 'settings.timeFormat24h',
}
const timeFormatOptions = computed(() =>
  (settings.value?.supported_time_formats ?? ['auto', '12h', '24h']).map((value) => ({
    value,
    label: t(timeFormatLabels[value] ?? 'settings.timeFormatAuto'),
  })),
)

/** The uptime windows the operator can choose from. */
const windowOptions = computed(() => uptimeWindowOptions())

/** String wrappers, because the Select and the number input speak strings. */
const uptimeWindow = computed({
  get: () => String(form.value.uptime_window_hours),
  set: (value: string) => {
    form.value.uptime_window_hours = Number(value)
  },
})
const retentionDays = computed({
  get: () => String(form.value.heartbeat_retention_days),
  set: (value: string) => {
    form.value.heartbeat_retention_days = Number(value)
  },
})

async function load(): Promise<void> {
  try {
    const [adminSettings, stats, expiry] = await Promise.all([
      api.adminSettings(),
      api.heartbeatRetention(),
      api.expirySettings(),
    ])
    settings.value = adminSettings
    retention.value = stats
    expiryForm.value = { ...expiry.settings }
    form.value = {
      default_locale: adminSettings.default_locale,
      default_theme: adminSettings.default_theme,
      time_format: adminSettings.time_format ?? 'auto',
      app_name: adminSettings.app_name,
      heartbeat_retention_days: adminSettings.heartbeat_retention_days,
      uptime_window_hours: adminSettings.uptime_window_hours,
    }
  } catch (error) {
    toast.error(t('common.error'), translateError(error))
  }
}

async function save(): Promise<void> {
  saving.value = true
  try {
    await api.updateAdminSettings(form.value)
    toast.success(t('common.saved'))
    await Promise.all([app.bootstrap(), load()])
  } catch (error) {
    toast.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

/** saveExpiry stores the certificate/domain job configuration. */
async function saveExpiry(): Promise<void> {
  expirySaved.value = true
  try {
    const saved = await api.updateExpirySettings({
      ...expiryForm.value,
      rate_limit_ms: toNumber(expiryForm.value.rate_limit_ms, 100),
      timeout_seconds: toNumber(expiryForm.value.timeout_seconds, 10),
    })
    expiryForm.value = { ...saved }
    toast.success(t('common.saved'))
  } catch (error) {
    toast.error(t('common.error'), translateError(error))
  } finally {
    expirySaved.value = false
  }
}

/** purge deletes the history older than the configured retention. */
async function purge(): Promise<void> {
  purging.value = true
  try {
    const result = await api.purgeHeartbeats(form.value.heartbeat_retention_days)
    toast.success(t('settings.purged', { count: result.deleted }))
    purgeOpen.value = false
    await load()
  } catch (error) {
    toast.error(t('common.error'), translateError(error))
  } finally {
    purging.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col gap-4">
    <header>
      <h1 class="text-lg font-semibold">{{ t('settings.title') }}</h1>
      <p class="text-xs text-muted-foreground">{{ t('settings.subtitle') }}</p>
    </header>

    <Card :title="t('settings.defaultsHelp')">
      <div class="grid gap-3 sm:grid-cols-3">
        <div class="grid gap-1">
          <Label for="settings-locale">{{ t('settings.defaultLocale') }}</Label>
          <Select id="settings-locale" v-model="form.default_locale" :options="localeOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="settings-theme">{{ t('settings.defaultTheme') }}</Label>
          <Select id="settings-theme" v-model="form.default_theme" :options="themeOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="settings-name">{{ t('settings.appName') }}</Label>
          <Input id="settings-name" v-model="form.app_name" />
        </div>
        <div class="grid gap-1">
          <Label for="settings-time-format" :help="t('settings.timeFormatHelp')">
            {{ t('settings.timeFormat') }}
          </Label>
          <Select id="settings-time-format" v-model="form.time_format" :options="timeFormatOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="settings-uptime-window" :help="t('settings.uptimeWindowHelp')">
            {{ t('settings.uptimeWindow') }}
          </Label>
          <Select id="settings-uptime-window" v-model="uptimeWindow" :options="windowOptions" />
        </div>
      </div>

      <template #footer>
        <Button size="sm" :loading="saving" @click="save">
          <Save class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.save') }}
        </Button>
      </template>
    </Card>

    <Card :title="t('settings.retention')" :description="t('settings.retentionHelp')">
      <div class="grid gap-3 sm:max-w-sm">
        <div class="grid gap-1">
          <Label
            for="settings-retention-days"
            :help="t('settings.retentionDaysHelp', { days: retention?.default_days ?? 180 })"
          >
            {{ t('settings.retentionDays') }}
          </Label>
          <Input
            id="settings-retention-days"
            v-model="retentionDays"
            type="number"
            min="0"
            :max="retention?.max_days ?? 3650"
          />
        </div>
      </div>

      <dl v-if="retention" class="mt-4 grid gap-3 text-xs sm:grid-cols-3">
        <div>
          <dt class="text-muted-foreground">{{ t('settings.retentionStored') }}</dt>
          <dd class="mt-0.5 tabular-nums">{{ retention.total }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('settings.retentionOldest') }}</dt>
          <dd class="mt-0.5">{{ formatDateTime(retention.oldest, locale) }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('settings.retentionWouldDelete') }}</dt>
          <dd class="mt-0.5 tabular-nums">{{ retention.would_delete }}</dd>
        </div>
      </dl>

      <Alert v-if="retention && retention.retention_days === 0" variant="warning" class="mt-3">
        {{ t('settings.retentionDisabled') }}
      </Alert>

      <template #footer>
        <Button size="sm" :loading="saving" @click="save">
          <Save class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.save') }}
        </Button>
        <Button variant="outline" size="sm" @click="purgeOpen = true">
          <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('settings.purgeNow') }}
        </Button>
      </template>
    </Card>

    <Card :title="t('settings.certificateCard')" :description="t('settings.certificateCardHelp')">
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label :help="t('expiry.checkTimeHelp')">{{ t('expiry.checkTime') }}</Label>
          <div class="flex items-center gap-2">
            <Select v-model="checkHour" :options="hourOptions" class="w-20" @update:model-value="setCheckHour" />
            <span class="text-sm text-muted-foreground">:</span>
            <Select v-model="checkMinute" :options="minuteOptions" class="w-20" @update:model-value="setCheckMinute" />
            <span class="font-mono text-xs text-muted-foreground">{{ expiryForm.check_time }}</span>
          </div>
        </div>
        <div class="grid gap-1">
          <Label for="settings-expiry-zone" :help="t('expiry.timezoneHelp')">{{ t('expiry.timezone') }}</Label>
          <Input
            id="settings-expiry-zone"
            v-model="expiryForm.check_timezone"
            list="expiry-timezones"
            placeholder="UTC"
          />
          <datalist id="expiry-timezones">
            <option v-for="zone in timezoneSuggestions" :key="zone" :value="zone" />
          </datalist>
        </div>
        <div class="grid gap-1">
          <Label for="settings-expiry-rate" :help="t('expiry.rateLimitHelp')">{{ t('expiry.rateLimit') }}</Label>
          <Input id="settings-expiry-rate" v-model.number="expiryForm.rate_limit_ms" type="number" min="0" max="60000" />
        </div>
        <div class="grid gap-1">
          <Label for="settings-expiry-timeout" :help="t('expiry.timeoutHelp')">{{ t('expiry.timeout') }}</Label>
          <Input
            id="settings-expiry-timeout"
            v-model.number="expiryForm.timeout_seconds"
            type="number"
            min="1"
            max="60"
          />
        </div>
        <div class="flex flex-col gap-2 sm:col-span-2">
          <Switch v-model="expiryForm.rdap_enabled as boolean">{{ t('expiry.rdapEnabled') }}</Switch>
          <Switch v-model="expiryForm.whois_enabled as boolean">{{ t('expiry.whoisEnabled') }}</Switch>
        </div>
      </div>

      <template #footer>
        <Button size="sm" :loading="expirySaved" @click="saveExpiry">
          <Save class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.save') }}
        </Button>
      </template>
    </Card>

    <LighthouseJobCard />

    <Card :title="t('settings.instanceInfo')">
      <dl class="grid gap-3 text-xs sm:grid-cols-2">
        <div>
          <dt class="text-muted-foreground">{{ t('app.instance') }}</dt>
          <dd class="mt-0.5 font-mono">{{ settings?.app_url }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('settings.authMethod') }}</dt>
          <dd class="mt-0.5">{{ settings?.auth_method }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('settings.clusterEnabled') }}</dt>
          <dd class="mt-0.5">{{ settings?.cluster_enabled ? t('common.yes') : t('common.no') }}</dd>
        </div>
        <div>
          <dt class="text-muted-foreground">{{ t('app.version') }}</dt>
          <dd class="mt-0.5">{{ settings?.version }}</dd>
        </div>
      </dl>
      <Alert variant="info" class="mt-3">{{ t('settings.defaultsHelp') }}</Alert>
    </Card>

    <ConfirmDialog
      v-model="purgeOpen"
      :title="t('settings.purgeTitle')"
      :description="t('settings.purgeWarning', { days: form.heartbeat_retention_days })"
      :confirm-label="t('settings.purgeNow')"
      :loading="purging"
      @confirm="purge"
    />
  </div>
</template>
