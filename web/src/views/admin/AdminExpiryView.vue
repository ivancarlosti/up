<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { FlaskConical, Pencil, Play, Plus, RefreshCw, RotateCcw, Save, Trash2 } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import SortHeader from '@/components/ui/SortHeader.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { expiryStateVariant } from '@/lib/expiry'
import { formatDateTime } from '@/lib/format'
import { expiryTargetSortKeys, sortExpiryTargets } from '@/lib/sort'
import type { ExpiryTargetSortKey, SortDirection } from '@/lib/sort'
import { useToastStore } from '@/stores/toast'
import type { ExpirySettings, ExpiryTarget, WhoisParser, WhoisParserPayload, WhoisTestResult } from '@/lib/types'

const { t, locale } = useI18n()
const toasts = useToastStore()

const loading = ref(false)
const saving = ref(false)
const running = ref(false)
const testing = ref(false)

const parsers = ref<WhoisParser[]>([])
const nextRun = ref<string | null>(null)

/**
 * The value a new rule starts with: ISO 8601, the layout most registries emit
 * (mirrors models.DefaultWhoisDateLayouts on the backend).
 */
const DEFAULT_DATE_LAYOUTS = '2006-01-02T15:04:05Z07:00;2006-01-02'

/** Text filter of the rules table (TLD or server). */
const parserSearch = ref('')

// The deduplicated worklist of the job: what a run would look up, with the last
// observation of each target.
const targets = ref<ExpiryTarget[]>([])
const targetsLoading = ref(false)
const refreshingTarget = ref('')

// Filters and sort of the worklist table. The sort choice is remembered per
// browser (the same pattern as the monitors table) so an operator who always
// sorts by "status" finds the table that way after a reload.
const targetTypeFilter = ref('')
const targetSearch = ref('')
const TARGET_SORT_STORAGE_KEY = 'up.admin.expiry.targets.sort'

function loadTargetSort(): { key: ExpiryTargetSortKey; direction: SortDirection } {
  const fallback: { key: ExpiryTargetSortKey; direction: SortDirection } = { key: 'kind', direction: 'asc' }
  try {
    const raw = localStorage.getItem(TARGET_SORT_STORAGE_KEY)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as { key?: ExpiryTargetSortKey; direction?: SortDirection }
    if (!parsed.key || !expiryTargetSortKeys.includes(parsed.key)) return fallback
    return { key: parsed.key, direction: parsed.direction === 'desc' ? 'desc' : 'asc' }
  } catch {
    return fallback
  }
}

const targetSort = ref(loadTargetSort())

/** toggleTargetSort switches the column, or flips the direction of the current one. */
function toggleTargetSort(key: ExpiryTargetSortKey): void {
  targetSort.value =
    targetSort.value.key === key
      ? { key, direction: targetSort.value.direction === 'asc' ? 'desc' : 'asc' }
      : { key, direction: 'asc' }
  try {
    localStorage.setItem(TARGET_SORT_STORAGE_KEY, JSON.stringify(targetSort.value))
  } catch {
    // A browser that refuses to persist (private mode) still sorts, it just
    // forgets the preference after a reload.
  }
}

/** targetTypeOptions adds the "all targets" entry to the two target kinds. */
const targetTypeOptions = computed(() => [
  { value: '', label: t('expiry.targetAllTypes') },
  { value: 'certificate', label: t('expiry.targetKindCertificate') },
  { value: 'domain', label: t('expiry.targetKindDomain') },
])

/** filteredTargets applies the type and text filters of the worklist. */
const filteredTargets = computed(() => {
  const term = targetSearch.value.trim().toLowerCase()
  return targets.value.filter((target) => {
    if (targetTypeFilter.value && target.kind !== targetTypeFilter.value) return false
    if (!term) return true
    const haystack = [
      target.label,
      target.server_name ?? '',
      target.address ?? '',
      target.domain ?? '',
      ...target.monitors.map((monitor) => monitor.name),
    ]
    return haystack.some((value) => value.toLowerCase().includes(term))
  })
})

/** sortedTargets applies the chosen column to the filtered rows (see lib/sort.ts). */
const sortedTargets = computed(() =>
  sortExpiryTargets(filteredTargets.value, targetSort.value.key, targetSort.value.direction, {
    locale: locale.value,
  }),
)

const settingsForm = reactive<ExpirySettings>(blankSettings())
const parserForm = reactive<WhoisParserPayload>(blankParser())
const editing = ref<WhoisParser | null>(null)
const dialogOpen = ref(false)

const confirmOpen = ref(false)
const pendingRemoval = ref<WhoisParser | null>(null)

// "Reset WHOIS parsers": the destructive action that reinstalls the built-in
// rules, so it has its own confirmation (see confirmReset).
const resetOpen = ref(false)
const resetBusy = ref(false)

const testDomain = ref('')
const testRaw = ref('')
const testResult = ref<WhoisTestResult | null>(null)

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

function blankSettings(): ExpirySettings {
  return {
    check_time: '03:00',
    check_timezone: 'UTC',
    rdap_enabled: true,
    whois_enabled: true,
    rate_limit_ms: 100,
    timeout_seconds: 10,
  }
}

function blankParser(): WhoisParserPayload {
  return {
    tld: '',
    server: '',
    expiry_regex: '',
    date_layouts: DEFAULT_DATE_LAYOUTS,
    not_found_pattern: '',
    min_interval_ms: 0,
    enabled: true,
    note: '',
  }
}

/** filteredParsers applies the text filter of the rules table. */
const filteredParsers = computed(() => {
  const term = parserSearch.value.trim().toLowerCase()
  if (!term) return parsers.value
  return parsers.value.filter((parser) =>
    [parser.tld, parser.server, parser.note].some((value) => (value ?? '').toLowerCase().includes(term)),
  )
})

/** toNumber converts what a native number input produces (a string). */
function toNumber(value: unknown, fallback: number): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

/** settingsPayload normalizes the numeric fields before they hit the API. */
function settingsPayload(): ExpirySettings {
  return {
    ...settingsForm,
    rate_limit_ms: toNumber(settingsForm.rate_limit_ms, 100),
    timeout_seconds: toNumber(settingsForm.timeout_seconds, 10),
  }
}

/** parserPayload normalizes the numeric field of a rule. */
function parserPayload(): WhoisParserPayload {
  return { ...parserForm, min_interval_ms: toNumber(parserForm.min_interval_ms, 0) }
}

const nextRunLabel = computed(() => (nextRun.value ? formatDateTime(nextRun.value, locale.value) : ''))

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
const checkHour = computed(() => settingsForm.check_time.split(':')[0] ?? '03')
const checkMinute = computed(() => settingsForm.check_time.split(':')[1] ?? '00')
function setCheckHour(value: string): void {
  settingsForm.check_time = `${value}:${checkMinute.value}`
}
function setCheckMinute(value: string): void {
  settingsForm.check_time = `${checkHour.value}:${value}`
}

async function load(): Promise<void> {
  loading.value = true
  try {
    const response = await api.expirySettings()
    Object.assign(settingsForm, blankSettings(), response.settings)
    parsers.value = response.parsers ?? []
    nextRun.value = response.next_run ?? null
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    loading.value = false
  }
  await loadTargets()
}

/** loadTargets refreshes the deduplicated worklist (no lookup is performed). */
async function loadTargets(): Promise<void> {
  targetsLoading.value = true
  try {
    targets.value = (await api.expiryTargets()).targets ?? []
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    targetsLoading.value = false
  }
}

/** refreshTarget is the "check now" of a single row of the worklist. */
async function refreshTarget(target: ExpiryTarget): Promise<void> {
  refreshingTarget.value = target.key
  try {
    const result = await api.refreshExpiryTarget({ kind: target.kind, target: target.key })
    toasts.success(t('expiry.targetRefreshed', { count: result.monitors }))
    await loadTargets()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    refreshingTarget.value = ''
  }
}

/** targetBadge is the label an operator reads: days left, or why there is none. */
function targetBadge(target: ExpiryTarget): string {
  if (typeof target.days_left === 'number') {
    const key = target.kind === 'domain' ? 'domain.daysLeft' : 'certificate.daysLeft'
    return t(key, { days: target.days_left })
  }
  switch (target.status) {
    case 'not_found':
      return t('domain.notFound')
    case 'unsupported':
      return t('domain.unsupported')
    case 'error':
      return t('domain.unavailable')
    default:
      return ''
  }
}

async function saveSettings(): Promise<void> {
  saving.value = true
  try {
    const saved = await api.updateExpirySettings(settingsPayload())
    Object.assign(settingsForm, blankSettings(), saved)
    toasts.success(t('common.saved'))
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

async function runNow(): Promise<void> {
  running.value = true
  try {
    await api.runExpiryNow()
    toasts.success(t('expiry.runDone'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    running.value = false
  }
}

function openCreate(): void {
  editing.value = null
  Object.assign(parserForm, blankParser())
  resetTest()
  dialogOpen.value = true
}

function openEdit(parser: WhoisParser): void {
  editing.value = parser
  Object.assign(parserForm, blankParser(), {
    tld: parser.tld,
    server: parser.server,
    expiry_regex: parser.expiry_regex,
    date_layouts: parser.date_layouts,
    not_found_pattern: parser.not_found_pattern,
    min_interval_ms: parser.min_interval_ms,
    enabled: parser.enabled,
    note: parser.note,
  })
  resetTest()
  dialogOpen.value = true
}

function resetTest(): void {
  testDomain.value = ''
  testRaw.value = ''
  testResult.value = null
}

async function saveParser(): Promise<void> {
  saving.value = true
  try {
    if (editing.value) {
      await api.updateWhoisParser(editing.value.id, parserPayload())
    } else {
      await api.createWhoisParser(parserPayload())
    }
    dialogOpen.value = false
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
    await api.deleteWhoisParser(pendingRemoval.value.id)
    toasts.success(t('common.deleted'))
    pendingRemoval.value = null
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

/**
 * confirmReset restores the built-in TLD rules: every custom rule (edits,
 * additions and deletions) is discarded, which is what the confirmation alert
 * asks for.
 */
async function confirmReset(): Promise<void> {
  resetBusy.value = true
  try {
    const result = await api.resetWhoisParsers()
    parsers.value = result.parsers ?? []
    toasts.success(t('expiry.resetParsersDone', { count: result.count }))
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    resetBusy.value = false
    resetOpen.value = false
  }
}

async function runTest(): Promise<void> {
  testing.value = true
  testResult.value = null
  try {
    testResult.value = await api.testWhoisParser({
      ...parserPayload(),
      domain: testDomain.value.trim(),
      raw: testRaw.value,
    })
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    testing.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col gap-4">
    <header class="flex flex-wrap items-end justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold">{{ t('expiry.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('expiry.subtitle') }}</p>
      </div>
      <Button size="sm" variant="outline" :loading="running" @click="runNow">
        <Play class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('expiry.runNow') }}
      </Button>
    </header>

    <Alert variant="info">
      {{ t('expiry.cadenceHelp') }}
      <span v-if="nextRunLabel"> {{ t('expiry.nextRun', { at: nextRunLabel }) }}</span>
    </Alert>

    <Card :title="t('expiry.settingsTitle')" :description="t('expiry.settingsHelp')">
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="expiry-hour" :help="t('expiry.checkTimeHelp')">{{ t('expiry.checkTime') }}</Label>
          <div class="flex items-center gap-2">
            <div class="w-20">
              <Select
                id="expiry-hour"
                :model-value="checkHour"
                :options="hourOptions"
                @update:model-value="setCheckHour($event as string)"
              />
            </div>
            <span class="text-sm text-muted-foreground">:</span>
            <div class="w-20">
              <Select
                id="expiry-minute"
                :model-value="checkMinute"
                :options="minuteOptions"
                @update:model-value="setCheckMinute($event as string)"
              />
            </div>
            <span class="font-mono text-xs text-muted-foreground">{{ settingsForm.check_time }}</span>
          </div>
        </div>
        <div class="grid gap-1">
          <Label for="expiry-zone" :help="t('expiry.timezoneHelp')">{{ t('expiry.timezone') }}</Label>
          <Input id="expiry-zone" v-model="settingsForm.check_timezone" list="expiry-timezones" placeholder="UTC" />
          <datalist id="expiry-timezones">
            <option v-for="zone in timezoneSuggestions" :key="zone" :value="zone" />
          </datalist>
        </div>
        <div class="grid gap-1">
          <Label for="expiry-rate" :help="t('expiry.rateLimitHelp')">{{ t('expiry.rateLimit') }}</Label>
          <Input id="expiry-rate" v-model.number="settingsForm.rate_limit_ms" type="number" min="0" max="60000" />
        </div>
        <div class="grid gap-1">
          <Label for="expiry-timeout" :help="t('expiry.timeoutHelp')">{{ t('expiry.timeout') }}</Label>
          <Input id="expiry-timeout" v-model.number="settingsForm.timeout_seconds" type="number" min="1" max="60" />
        </div>
        <div class="flex flex-col gap-2 sm:col-span-2">
          <Switch v-model="settingsForm.rdap_enabled as boolean">{{ t('expiry.rdapEnabled') }}</Switch>
          <Switch v-model="settingsForm.whois_enabled as boolean">{{ t('expiry.whoisEnabled') }}</Switch>
        </div>
      </div>
      <template #footer>
        <Button size="sm" :loading="saving" @click="saveSettings">
          <Save class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.save') }}
        </Button>
      </template>
    </Card>

    <Card :title="t('expiry.parsersTitle')" :description="t('expiry.parsersHelp')">
      <template #actions>
        <Button
          variant="outline"
          size="sm"
          class="text-status-down"
          :title="t('expiry.resetParsersTitle')"
          @click="resetOpen = true"
        >
          <RotateCcw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('expiry.resetParsers') }}
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('expiry.newParser') }}
        </Button>
      </template>

      <div v-if="parsers.length" class="mb-3 flex flex-wrap items-center gap-2">
        <Input v-model="parserSearch" class="max-w-xs" :placeholder="t('expiry.parserSearchPlaceholder')" />
        <span class="text-[11px] text-muted-foreground">
          {{ t('expiry.parsersCount', { shown: filteredParsers.length, total: parsers.length }) }}
        </span>
      </div>

      <EmptyState
        v-if="!parsers.length && !loading"
        :title="t('expiry.noParsers')"
        :description="t('expiry.noParsersHelp')"
      />

      <p v-else-if="!filteredParsers.length" class="text-sm text-muted-foreground">
        {{ t('expiry.noParsersMatch') }}
      </p>

      <div v-else class="overflow-x-auto">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('expiry.tld') }}</th>
              <th>{{ t('expiry.server') }}</th>
              <th>{{ t('expiry.rateOverride') }}</th>
              <th>{{ t('common.status') }}</th>
              <th class="text-end">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="parser in filteredParsers" :key="parser.id">
              <td>
                <span class="font-mono">.{{ parser.tld }}</span>
                <span v-if="parser.note" class="block text-[11px] text-muted-foreground">{{ parser.note }}</span>
              </td>
              <td class="font-mono">{{ parser.server || t('expiry.serverAuto') }}</td>
              <td>
                {{ parser.min_interval_ms ? `${parser.min_interval_ms} ms` : t('expiry.rateGlobal') }}
              </td>
              <td>
                <Badge :variant="parser.enabled ? 'success' : 'secondary'">
                  {{ parser.enabled ? t('common.enabled') : t('common.disabled') }}
                </Badge>
              </td>
              <td class="text-end">
                <div class="flex items-center justify-end gap-1">
                  <Button variant="ghost" size="sm" :title="t('common.edit')" @click="openEdit(parser)">
                    <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="text-status-down"
                    :title="t('common.delete')"
                    @click="(pendingRemoval = parser), (confirmOpen = true)"
                  >
                    <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </Card>

    <Card :title="t('expiry.targetsTitle')" :description="t('expiry.targetsHelp')">
      <template #actions>
        <Button variant="outline" size="sm" :loading="targetsLoading" @click="loadTargets">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
      </template>

      <div v-if="targets.length" class="mb-3 flex flex-wrap items-center gap-2">
        <Select v-model="targetTypeFilter" class="w-44" :options="targetTypeOptions" />
        <Input v-model="targetSearch" class="max-w-xs" :placeholder="t('expiry.targetSearchPlaceholder')" />
        <span class="text-[11px] text-muted-foreground">
          {{ t('expiry.targetCount', { shown: filteredTargets.length, total: targets.length }) }}
        </span>
      </div>

      <EmptyState
        v-if="!targets.length && !targetsLoading"
        :title="t('expiry.noTargets')"
        :description="t('expiry.noTargetsHelp')"
      />
      <p v-else-if="!filteredTargets.length" class="text-xs text-muted-foreground">
        {{ t('expiry.noTargetsMatch') }}
      </p>

      <div v-else class="overflow-x-auto">
        <table class="data-table">
          <thead>
            <tr>
              <SortHeader
                :label="t('common.type')"
                column="kind"
                :active="targetSort.key"
                :direction="targetSort.direction"
                @toggle="toggleTargetSort('kind')"
              />
              <SortHeader
                :label="t('expiry.targetLabel')"
                column="label"
                :active="targetSort.key"
                :direction="targetSort.direction"
                @toggle="toggleTargetSort('label')"
              />
              <SortHeader
                :label="t('expiry.targetMonitorsLabel')"
                column="monitors"
                :active="targetSort.key"
                :direction="targetSort.direction"
                @toggle="toggleTargetSort('monitors')"
              />
              <SortHeader
                :label="t('common.status')"
                column="status"
                :active="targetSort.key"
                :direction="targetSort.direction"
                @toggle="toggleTargetSort('status')"
              />
              <SortHeader
                :label="t('expiry.targetLastCheck')"
                column="checked"
                :active="targetSort.key"
                :direction="targetSort.direction"
                @toggle="toggleTargetSort('checked')"
              />
              <th class="text-end">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="target in sortedTargets" :key="`${target.kind}:${target.key}`">
              <td class="whitespace-nowrap">
                <Badge variant="secondary">
                  {{ target.kind === 'domain' ? t('expiry.targetKindDomain') : t('expiry.targetKindCertificate') }}
                </Badge>
                <Badge v-if="target.manual" variant="outline" class="ms-1">{{ t('expiry.targetManual') }}</Badge>
              </td>
              <td class="min-w-[16rem]">
                <span class="block break-all font-mono">{{ target.label }}</span>
                <span
                  v-if="target.server_name && target.server_name !== target.address"
                  class="block text-[11px] text-muted-foreground"
                >
                  {{ target.server_name }}
                </span>
                <span class="block text-[11px] text-muted-foreground">
                  {{ target.monitors.map((monitor) => monitor.name).join(', ') }}
                </span>
              </td>
              <td class="whitespace-nowrap tabular-nums">{{ target.monitors.length }}</td>
              <td class="whitespace-nowrap">
                <Badge
                  v-if="targetBadge(target)"
                  :variant="expiryStateVariant(target.status, target.days_left)"
                >
                  {{ targetBadge(target) }}
                </Badge>
                <span v-else class="text-muted-foreground">{{ t('expiry.targetPending') }}</span>
              </td>
              <td class="whitespace-nowrap text-muted-foreground">
                {{ target.checked_at ? formatDateTime(target.checked_at, locale) : '—' }}
              </td>
              <td class="text-end">
                <Button
                  variant="ghost"
                  size="sm"
                  :loading="refreshingTarget === target.key"
                  :title="t('expiry.targetRefresh')"
                  :aria-label="t('expiry.targetRefresh')"
                  @click="refreshTarget(target)"
                >
                  <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </Card>

    <Dialog v-model="dialogOpen" :title="editing ? t('expiry.editParser') : t('expiry.newParser')" wide>
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="parser-tld" required :help="t('expiry.tldHelp')">{{ t('expiry.tld') }}</Label>
          <Input id="parser-tld" v-model="parserForm.tld" placeholder="com.br" />
        </div>
        <div class="grid gap-1">
          <Label for="parser-server" :help="t('expiry.serverHelp')">{{ t('expiry.server') }}</Label>
          <Input id="parser-server" v-model="parserForm.server" placeholder="whois.registro.br" />
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="parser-regex" required :help="t('expiry.regexHelp')">{{ t('expiry.regex') }}</Label>
          <Input id="parser-regex" v-model="parserForm.expiry_regex" placeholder="expires at:\s*(.+)" />
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="parser-layouts" :help="t('expiry.layoutsHelp')">{{ t('expiry.layouts') }}</Label>
          <Input id="parser-layouts" v-model="parserForm.date_layouts" placeholder="2006-01-02T15:04:05Z07:00" />
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="parser-notfound" :help="t('expiry.notFoundHelp')">{{ t('expiry.notFound') }}</Label>
          <Input id="parser-notfound" v-model="parserForm.not_found_pattern" placeholder="(?i)no match for" />
        </div>
        <div class="grid gap-1">
          <Label for="parser-interval" :help="t('expiry.rateOverrideHelp')">{{ t('expiry.rateOverride') }}</Label>
          <Input id="parser-interval" v-model.number="parserForm.min_interval_ms" type="number" min="0" max="60000" />
        </div>
        <div class="grid gap-1">
          <Label for="parser-note">{{ t('common.description') }}</Label>
          <Input id="parser-note" v-model="parserForm.note" />
        </div>
        <div class="sm:col-span-2">
          <Switch v-model="parserForm.enabled as boolean">{{ t('common.enabled') }}</Switch>
        </div>
      </div>

      <div class="mt-4 grid gap-2 border-t border-border pt-4">
        <Label :help="t('expiry.testHelp')">{{ t('expiry.testTitle') }}</Label>
        <div class="flex flex-wrap items-end gap-2">
          <div class="grid min-w-48 flex-1 gap-1">
            <Label for="parser-test-domain">{{ t('expiry.testDomain') }}</Label>
            <Input id="parser-test-domain" v-model="testDomain" placeholder="example.com.br" />
          </div>
          <Button size="sm" variant="outline" :loading="testing" @click="runTest">
            <FlaskConical class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('common.test') }}
          </Button>
        </div>
        <div class="grid gap-1">
          <Label for="parser-test-raw" :help="t('expiry.testRawHelp')">{{ t('expiry.testRaw') }}</Label>
          <Textarea id="parser-test-raw" v-model="testRaw" :rows="3" placeholder="expires at: 2027-05-01" />
        </div>
        <Alert v-if="testResult" :variant="testResult.ok ? 'success' : 'danger'">
          <template v-if="testResult.not_found">{{ t('expiry.testNotFound') }}</template>
          <template v-else-if="testResult.ok">
            {{
              t('expiry.testParsed', {
                days: testResult.days_left ?? 0,
                date: testResult.expires_at ? new Date(testResult.expires_at).toLocaleDateString() : '—',
              })
            }}
          </template>
          <template v-else>{{ testResult.error }}</template>
        </Alert>
        <details v-if="testResult?.raw" class="text-[11px] text-muted-foreground">
          <summary class="cursor-pointer">{{ t('expiry.testRawResponse') }}</summary>
          <pre class="mt-1 max-h-40 overflow-auto whitespace-pre-wrap font-mono">{{ testResult.raw }}</pre>
        </details>
      </div>

      <template #footer>
        <Button variant="outline" @click="dialogOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="saveParser">{{ t('common.save') }}</Button>
      </template>
    </Dialog>

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('expiry.deleteParserTitle')"
      :description="t('expiry.deleteParserWarning')"
      :confirm-label="t('common.delete')"
      @confirm="confirmRemove"
    />

    <ConfirmDialog
      v-model="resetOpen"
      :title="t('expiry.resetParsersTitle')"
      :description="t('expiry.resetParsersWarning')"
      :confirm-label="t('expiry.resetParsersConfirm')"
      :loading="resetBusy"
      @confirm="confirmReset"
    />
  </div>
</template>
