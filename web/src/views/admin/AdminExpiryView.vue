<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { FlaskConical, Pencil, Play, Plus, Save, Trash2 } from 'lucide-vue-next'
import Alert from '@/components/ui/Alert.vue'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { useToastStore } from '@/stores/toast'
import type { ExpirySettings, WhoisParser, WhoisParserPayload, WhoisTestResult } from '@/lib/types'

const { t } = useI18n()
const toasts = useToastStore()

const loading = ref(false)
const saving = ref(false)
const running = ref(false)
const testing = ref(false)

const parsers = ref<WhoisParser[]>([])
const nextRun = ref<string | null>(null)

const settingsForm = reactive<ExpirySettings>(blankSettings())
const parserForm = reactive<WhoisParserPayload>(blankParser())
const editing = ref<WhoisParser | null>(null)
const dialogOpen = ref(false)

const confirmOpen = ref(false)
const pendingRemoval = ref<WhoisParser | null>(null)

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
    date_layouts: '',
    not_found_pattern: '',
    min_interval_ms: 0,
    enabled: true,
    note: '',
  }
}

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

const nextRunLabel = computed(() => {
  if (!nextRun.value) return ''
  return new Date(nextRun.value).toLocaleString()
})

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
          <Label for="expiry-time" :help="t('expiry.checkTimeHelp')">{{ t('expiry.checkTime') }}</Label>
          <Input id="expiry-time" v-model="settingsForm.check_time" type="time" />
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
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('expiry.newParser') }}
        </Button>
      </template>

      <EmptyState
        v-if="!parsers.length && !loading"
        :title="t('expiry.noParsers')"
        :description="t('expiry.noParsersHelp')"
      />

      <div v-else class="overflow-x-auto">
        <table class="w-full text-xs">
          <thead class="text-left text-muted-foreground">
            <tr class="border-b border-border">
              <th class="py-2 pr-3 font-medium">{{ t('expiry.tld') }}</th>
              <th class="py-2 pr-3 font-medium">{{ t('expiry.server') }}</th>
              <th class="py-2 pr-3 font-medium">{{ t('expiry.rateOverride') }}</th>
              <th class="py-2 pr-3 font-medium">{{ t('common.status') }}</th>
              <th class="py-2 text-right font-medium">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="parser in parsers" :key="parser.id" class="border-b border-border/60 last:border-0">
              <td class="py-2 pr-3 font-mono">.{{ parser.tld }}</td>
              <td class="py-2 pr-3 font-mono">{{ parser.server || t('expiry.serverAuto') }}</td>
              <td class="py-2 pr-3">
                {{ parser.min_interval_ms ? `${parser.min_interval_ms} ms` : t('expiry.rateGlobal') }}
              </td>
              <td class="py-2 pr-3">
                <Badge :variant="parser.enabled ? 'success' : 'secondary'">
                  {{ parser.enabled ? t('common.enabled') : t('common.disabled') }}
                </Badge>
              </td>
              <td class="py-2 text-right">
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
          <Input id="parser-layouts" v-model="parserForm.date_layouts" placeholder="2006-01-02" />
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
  </div>
</template>
