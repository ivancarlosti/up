<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import MonitorConfigFields from '@/components/monitors/MonitorConfigFields.vue'
import {
  sanitizeMonitorPayload,
  supportsCertificate as typeSupportsCertificate,
  supportsDomainWatch as typeSupportsDomainWatch,
} from '@/lib/monitor-config'
import type { Monitor, MonitorConfig, MonitorPayload, MonitorType, MonitorGroup, MonitorTemplate, Notification } from '@/lib/types'

const props = defineProps<{
  modelValue: boolean
  monitor: Monitor | null
  notifications: Notification[]
  groups?: MonitorGroup[]
  templates?: MonitorTemplate[]
  saving?: boolean
}>()

const emit = defineEmits<{ 'update:modelValue': [value: boolean]; submit: [payload: MonitorPayload] }>()
const { t } = useI18n()

/** Empty template used for the "new monitor" dialog. */
function emptyForm(): MonitorPayload {
  return {
    name: '',
    type: 'http',
    active: true,
    description: '',
    interval_seconds: 60,
    retries: 0,
    retries_interval_seconds: 60,
    timeout_seconds: 10,
    resend_interval_seconds: 0,
    upside_down: false,
    run_on: 'all',
    run_on_nodes: '',
    node_id: '',
    tags: '',
    config: { method: 'GET', encoding: 'json', auth_type: 'none', accepted_status_codes: '200-299', max_redirects: 10, record_type: 'A', resolver_server: '1.1.1.1', headers: [] },
    notification_ids: [],
    group_ids: [],
    template_uuid: '',
    cert_watch: false,
    cert_notify: false,
    cert_warn_days: '',
    domain_watch: false,
    domain_notify: false,
    domain_warn_days: '',
    domain_expires_at: null,
  }
}

const form = reactive<MonitorPayload>(emptyForm())

watch(
  () => [props.modelValue, props.monitor] as const,
  ([open]) => {
    if (!open) return
    Object.assign(form, emptyForm(), props.monitor ? JSON.parse(JSON.stringify(props.monitor)) : {})
    if (!form.config) form.config = {}
    if (!form.config.headers) form.config.headers = []
    // A date input only understands "YYYY-MM-DD"; the API stores an RFC3339
    // timestamp (or null).
    form.domain_expires_at = toDateInput(form.domain_expires_at)
  },
  { immediate: true },
)

const type = computed(() => (form.type ?? 'http') as MonitorType)
const config = computed<MonitorConfig>(() => form.config ?? {})

const typeOptions = computed(() => [
  { value: 'http', label: t('monitor.typeHttp') },
  { value: 'keyword', label: t('monitor.typeKeyword') },
  { value: 'tcp', label: t('monitor.typeTcp') },
  { value: 'dns', label: t('monitor.typeDns') },
  { value: 'ssl', label: t('monitor.typeSsl') },
])

/**
 * The capability predicates live in `@/lib/monitor-config` so the form and the
 * payload sanitizer can never disagree about what a type supports.
 */
const supportsCertificate = computed(() => typeSupportsCertificate(type.value))

/** Every type has a target a registrable domain can be derived from. */
const supportsDomain = computed(() => typeSupportsDomainWatch(type.value))

/**
 * Type of the template the monitor follows, when the template list is available:
 * the sanitizer uses it to drop a link the selected type cannot follow.
 */
const linkedTemplateType = computed<MonitorType | null>(
  () => (props.templates ?? []).find((template) => template.uuid === form.template_uuid)?.type ?? null,
)

const runOnOptions = computed(() => [
  { value: 'all', label: t('monitor.runOnAll') },
  { value: 'primary', label: t('monitor.runOnPrimary') },
  { value: 'node', label: t('monitor.runOnSpecific') },
  { value: 'some', label: t('monitor.runOnSome') },
])

/** Template options: a monitor can follow a reusable blueprint. */
const templateOptions = computed(() => [
  { value: '', label: t('monitor.templateNone') },
  ...(props.templates ?? []).map((template) => ({
    value: template.uuid,
    label: `${template.name} (${template.type})`,
  })),
])

/**
 * withProbeTarget copies the template probe options but keeps the target of the
 * monitor, exactly like the backend does: applying a template never repoints a
 * monitor at another address.
 *
 * The address of the monitor wins **when it has one**; the target of the template
 * stays otherwise, which is what makes the port of a tcp/ssl blueprint work (the
 * form of a new monitor has no address yet, and the port of the template is the
 * fallback the bulk importer documents as well).
 */
function withProbeTarget(config: MonitorConfig, target: MonitorConfig, monitorType: MonitorType): MonitorConfig {
  const next: MonitorConfig = { ...config }
  switch (monitorType) {
    case 'http':
    case 'keyword':
      if (target.url) next.url = target.url
      break
    case 'tcp':
    case 'ssl':
      // Both are host:port probes: the SNI and the TLS switches of a ssl
      // template are options, the address belongs to the monitor.
      if (target.host) next.host = target.host
      if (target.port) next.port = target.port
      break
    case 'dns':
      if (target.hostname) next.hostname = target.hostname
      break
  }
  return next
}

/** onTemplateChange links the monitor and prefills the blueprint defaults. */
function onTemplateChange(uuid: string): void {
  form.template_uuid = uuid
  const template = (props.templates ?? []).find((candidate) => candidate.uuid === uuid)
  if (!template) return
  const defaults = template.defaults
  form.type = template.type
  form.config = withProbeTarget(template.config, config.value, template.type)
  form.interval_seconds = defaults.interval_seconds
  form.retries = defaults.retries
  form.retries_interval_seconds = defaults.retries_interval_seconds
  form.timeout_seconds = defaults.timeout_seconds
  form.resend_interval_seconds = defaults.resend_interval_seconds
  form.run_on = defaults.run_on
  form.run_on_nodes = defaults.run_on_nodes
  form.node_id = defaults.node_id
  if (defaults.description) form.description = defaults.description
  if (defaults.active !== undefined && defaults.active !== null) form.active = defaults.active
  form.notification_ids = [...(defaults.notification_ids ?? [])]
  form.cert_watch = defaults.cert_watch
  form.cert_notify = defaults.cert_notify
  form.cert_warn_days = defaults.cert_warn_days
  form.domain_watch = defaults.domain_watch
  form.domain_notify = defaults.domain_notify
  form.domain_warn_days = defaults.domain_warn_days
}

function toggleNotification(id: number, value: boolean): void {
  const ids = new Set(form.notification_ids ?? [])
  if (value) ids.add(id)
  else ids.delete(id)
  form.notification_ids = [...ids]
}

function toggleGroup(id: number, value: boolean): void {
  const ids = new Set(form.group_ids ?? [])
  if (value) ids.add(id)
  else ids.delete(id)
  form.group_ids = [...ids]
}

/** toNumber converts what a native number input produces (a string). */
function toNumber(value: unknown, fallback: number): number {
  const parsed = Number(value)
  return Number.isFinite(parsed) ? parsed : fallback
}

/** toDateInput keeps only the "YYYY-MM-DD" part of a stored timestamp. */
function toDateInput(value: unknown): string {
  const raw = typeof value === 'string' ? value : ''
  return raw.length >= 10 ? raw.slice(0, 10) : ''
}

function submit(): void {
  const payload: MonitorPayload = JSON.parse(JSON.stringify(form))
  payload.config = payload.config ?? {}
  // Native `type="number"` inputs emit strings and the API decodes into ints:
  // without this coercion saving a monitor was rejected with
  // "cannot unmarshal string into ... of type int".
  payload.interval_seconds = toNumber(form.interval_seconds, 60)
  payload.timeout_seconds = toNumber(form.timeout_seconds, 10)
  payload.retries = toNumber(form.retries, 0)
  payload.retries_interval_seconds = toNumber(form.retries_interval_seconds, 60)
  payload.resend_interval_seconds = toNumber(form.resend_interval_seconds, 0)
  const config = payload.config
  if (config.port !== undefined && config.port !== null && String(config.port) !== '') {
    config.port = toNumber(config.port, 0)
  } else {
    delete config.port
  }
  if (config.max_redirects !== undefined && config.max_redirects !== null && String(config.max_redirects) !== '') {
    config.max_redirects = toNumber(config.max_redirects, 10)
  } else {
    delete config.max_redirects
  }
  payload.cert_warn_days = (form.cert_warn_days ?? '').trim()
  payload.domain_warn_days = (form.domain_warn_days ?? '').trim()
  // The API stores a timestamp (or null); the input yields a plain date.
  const manualDate = String(form.domain_expires_at ?? '').trim()
  payload.domain_expires_at = manualDate ? `${manualDate.slice(0, 10)}T00:00:00Z` : null
  // Switching the type hides the sections of the old one but keeps their values:
  // sending them back is what the API rejects ("cert_watch is only available for
  // http, keyword and ssl monitors"), so the payload only carries what the
  // selected type actually reads.
  emit('submit', sanitizeMonitorPayload(payload, { templateType: linkedTemplateType.value }))
}
</script>

<template>
  <Dialog
    :model-value="props.modelValue"
    :title="props.monitor ? t('monitor.edit') : t('monitor.new')"
    wide
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="grid gap-5">
      <!-- Template link -->
      <section v-if="props.templates?.length" class="grid gap-2">
        <Label for="monitor-template" :help="t('monitor.templateHelp')">
          {{ t('monitor.templateSection') }}
        </Label>
        <Select
          id="monitor-template"
          :model-value="form.template_uuid ?? ''"
          :options="templateOptions"
          @update:model-value="onTemplateChange($event as string)"
        />
        <p v-if="form.template_uuid" class="text-[11px] text-muted-foreground">
          {{ t('monitor.templateLinked') }}
        </p>
      </section>

      <!-- General -->
      <section class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="monitor-name" required>{{ t('common.name') }}</Label>
          <Input id="monitor-name" v-model="form.name" :placeholder="t('monitor.namePlaceholder')" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-type">{{ t('common.type') }}</Label>
          <Select id="monitor-type" v-model="form.type" :options="typeOptions" />
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="monitor-description">{{ t('common.description') }}</Label>
          <Input id="monitor-description" v-model="form.description" :placeholder="t('monitor.descriptionPlaceholder')" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-tags">{{ t('common.tags') }}</Label>
          <Input id="monitor-tags" v-model="form.tags" :placeholder="t('monitor.tagsPlaceholder')" />
        </div>
        <div class="flex items-end pb-1">
          <Switch v-model="form.active as boolean">{{ t('common.active') }}</Switch>
        </div>
      </section>

      <!-- Probe options: shared with the template form so a template can
           never drift from a monitor. -->
      <MonitorConfigFields :type="type" :config="config" />

      <!-- Scheduling -->
      <section class="grid gap-3 border-t border-border pt-4 sm:grid-cols-3">
        <div class="grid gap-1">
          <Label for="monitor-interval" required :help="t('monitor.intervalHelp')">{{ t('common.interval') }}</Label>
          <Input id="monitor-interval" v-model="form.interval_seconds" type="number" min="5" max="86400" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-timeout" :help="t('monitor.timeoutHelp')">{{ t('common.timeout') }}</Label>
          <Input id="monitor-timeout" v-model="form.timeout_seconds" type="number" min="1" max="300" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-retries" :help="t('monitor.retriesHelp')">{{ t('common.retries') }}</Label>
          <Input id="monitor-retries" v-model="form.retries" type="number" min="0" max="50" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-retries-interval">{{ t('monitor.retriesInterval') }}</Label>
          <Input id="monitor-retries-interval" v-model="form.retries_interval_seconds" type="number" min="1" max="3600" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-resend" :help="t('monitor.resendHelp')">{{ t('monitor.resendInterval') }}</Label>
          <Input id="monitor-resend" v-model="form.resend_interval_seconds" type="number" min="0" />
        </div>
        <div class="flex items-end pb-1">
          <Switch v-model="form.upside_down as boolean">{{ t('monitor.upsideDown') }}</Switch>
        </div>
        <div class="grid gap-1">
          <Label for="monitor-runon" :help="t('monitor.runOnHelp')">{{ t('monitor.runOn') }}</Label>
          <Select id="monitor-runon" v-model="form.run_on" :options="runOnOptions" />
        </div>
        <div v-if="form.run_on === 'node'" class="grid gap-1">
          <Label for="monitor-node">{{ t('monitor.targetNode') }}</Label>
          <Input id="monitor-node" v-model="form.node_id" placeholder="up-node-2" />
        </div>
        <div v-if="form.run_on === 'some'" class="grid gap-1">
          <Label for="monitor-nodes" :help="t('monitor.runOnSomeHelp')">{{ t('monitor.targetNodes') }}</Label>
          <Input id="monitor-nodes" v-model="form.run_on_nodes as string" placeholder="up-node-1, up-node-2" />
        </div>
      </section>

      <!-- Notification channels -->
      <section v-if="props.notifications.length" class="grid gap-2 border-t border-border pt-4">
        <Label>{{ t('monitor.notificationsSection') }}</Label>
        <div class="flex flex-wrap gap-4">
          <Checkbox
            v-for="channel in props.notifications"
            :key="channel.id"
            :model-value="(form.notification_ids ?? []).includes(channel.id)"
            @update:model-value="toggleNotification(channel.id, $event)"
          >
            {{ channel.name }} ({{ channel.type }})
          </Checkbox>
        </div>
      </section>

      <!-- Certificate -->
      <section v-if="supportsCertificate" id="monitor-certificate" class="grid gap-2 border-t border-border pt-4">
        <Label :help="t('monitor.certHelp')">{{ t('monitor.certSection') }}</Label>
        <div class="flex flex-wrap items-center gap-4">
          <Switch v-model="form.cert_watch as boolean">{{ t('monitor.certWatch') }}</Switch>
          <Switch v-model="form.cert_notify as boolean">{{ t('monitor.certNotify') }}</Switch>
        </div>
        <div v-if="form.cert_watch" class="grid gap-1 sm:max-w-sm">
          <Label for="monitor-cert-warn" :help="t('monitor.certWarnDaysHelp')">{{ t('monitor.certWarnDays') }}</Label>
          <Input id="monitor-cert-warn" v-model="form.cert_warn_days" placeholder="30,14,7,1" />
        </div>
      </section>

      <!-- Domain expiration -->
      <section v-if="supportsDomain" id="monitor-domain" class="grid gap-2 border-t border-border pt-4">
        <Label :help="t('monitor.domainHelp')">{{ t('monitor.domainSection') }}</Label>
        <div class="flex flex-wrap items-center gap-4">
          <Switch v-model="form.domain_watch as boolean">{{ t('monitor.domainWatch') }}</Switch>
          <Switch v-model="form.domain_notify as boolean">{{ t('monitor.domainNotify') }}</Switch>
        </div>
        <div v-if="form.domain_watch" class="grid gap-3 sm:grid-cols-2">
          <div class="grid gap-1 sm:max-w-sm">
            <Label for="monitor-domain-warn" :help="t('monitor.domainWarnDaysHelp')">
              {{ t('monitor.domainWarnDays') }}
            </Label>
            <Input id="monitor-domain-warn" v-model="form.domain_warn_days" placeholder="30,14,7,1" />
          </div>
          <div class="grid gap-1 sm:max-w-sm">
            <Label for="monitor-domain-expires" :help="t('monitor.domainExpiresAtHelp')">
              {{ t('monitor.domainExpiresAt') }}
            </Label>
            <Input id="monitor-domain-expires" v-model="form.domain_expires_at as string" type="date" />
          </div>
        </div>
      </section>

      <!-- Groups -->
      <section v-if="props.groups?.length" class="grid gap-2 border-t border-border pt-4">
        <Label :help="t('monitor.groupsHelp')">{{ t('monitor.groupsSection') }}</Label>
        <div class="flex flex-wrap gap-4">
          <Checkbox
            v-for="group in props.groups"
            :key="group.id"
            :model-value="(form.group_ids ?? []).includes(group.id)"
            @update:model-value="toggleGroup(group.id, $event)"
          >
            {{ group.name }}
          </Checkbox>
        </div>
      </section>
    </div>

    <template #footer>
      <Button variant="outline" @click="emit('update:modelValue', false)">{{ t('common.cancel') }}</Button>
      <Button :loading="props.saving" @click="submit">{{ t('common.save') }}</Button>
    </template>
  </Dialog>
</template>

