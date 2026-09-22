<script setup lang="ts">
import { computed, reactive, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Plus, Trash2 } from 'lucide-vue-next'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import type { Header, Monitor, MonitorConfig, MonitorPayload, MonitorType, Notification } from '@/lib/types'

const props = defineProps<{
  modelValue: boolean
  monitor: Monitor | null
  notifications: Notification[]
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
    node_id: '',
    tags: '',
    config: { method: 'GET', encoding: 'json', auth_type: 'none', accepted_status_codes: '200-299', max_redirects: 10, record_type: 'A', resolver_server: '1.1.1.1', headers: [] },
    notification_ids: [],
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
])

const methodOptions = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'].map((value) => ({ value, label: value }))
const encodingOptions = ['json', 'form', 'xml', 'raw'].map((value) => ({ value, label: value }))
const recordTypeOptions = ['A', 'AAAA', 'CNAME', 'MX', 'TXT', 'NS', 'SOA'].map((value) => ({ value, label: value }))
const authOptions = computed(() => [
  { value: 'none', label: t('monitor.authNone') },
  { value: 'basic', label: t('monitor.authBasic') },
  { value: 'bearer', label: t('monitor.authBearer') },
])
const runOnOptions = computed(() => [
  { value: 'all', label: t('monitor.runOnAll') },
  { value: 'primary', label: t('monitor.runOnPrimary') },
  { value: 'node', label: t('monitor.runOnSpecific') },
])

const isHttpLike = computed(() => type.value === 'http' || type.value === 'keyword')

function addHeader(): void {
  if (!config.value.headers) config.value.headers = []
  config.value.headers.push({ key: '', value: '' } as Header)
}

function removeHeader(index: number): void {
  config.value.headers?.splice(index, 1)
}

function toggleNotification(id: number, value: boolean): void {
  const ids = new Set(form.notification_ids ?? [])
  if (value) ids.add(id)
  else ids.delete(id)
  form.notification_ids = [...ids]
}

function submit(): void {
  const payload: MonitorPayload = JSON.parse(JSON.stringify(form))
  payload.config = payload.config ?? {}
  if (!isHttpLike.value) {
    payload.config.headers = []
  }
  emit('submit', payload)
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

      <!-- HTTP(s) / Keyword -->
      <section v-if="isHttpLike" class="grid gap-3 border-t border-border pt-4 sm:grid-cols-2">
        <div class="grid gap-1 sm:col-span-2">
          <Label for="monitor-url" required>{{ t('monitor.url') }}</Label>
          <Input id="monitor-url" v-model="config.url" placeholder="https://example.com/health" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-method">{{ t('monitor.method') }}</Label>
          <Select id="monitor-method" v-model="config.method" :options="methodOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-encoding">{{ t('monitor.encoding') }}</Label>
          <Select id="monitor-encoding" v-model="config.encoding" :options="encodingOptions" />
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="monitor-body">{{ t('monitor.body') }}</Label>
          <Textarea id="monitor-body" v-model="config.body" :rows="3" />
        </div>

        <div class="grid gap-2 sm:col-span-2">
          <Label>{{ t('monitor.headers') }}</Label>
          <div v-for="(header, index) in config.headers ?? []" :key="index" class="flex items-center gap-2">
            <Input v-model="header.key" :placeholder="t('monitor.headerKey')" />
            <Input v-model="header.value" :placeholder="t('monitor.headerValue')" />
            <Button variant="ghost" size="icon" @click="removeHeader(index)">
              <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
            </Button>
          </div>
          <Button variant="outline" size="sm" @click="addHeader">
            <Plus class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('monitor.addHeader') }}
          </Button>
        </div>

        <div class="grid gap-1">
          <Label for="monitor-auth">{{ t('monitor.authentication') }}</Label>
          <Select id="monitor-auth" v-model="config.auth_type" :options="authOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-codes">{{ t('monitor.acceptedCodes') }}</Label>
          <Input id="monitor-codes" v-model="config.accepted_status_codes" placeholder="200-299" />
          <span class="text-[11px] text-muted-foreground">{{ t('monitor.acceptedCodesHelp') }}</span>
        </div>
        <div v-if="config.auth_type === 'basic'" class="grid gap-1">
          <Label for="monitor-user">{{ t('monitor.username') }}</Label>
          <Input id="monitor-user" v-model="config.basic_user" />
        </div>
        <div v-if="config.auth_type === 'basic'" class="grid gap-1">
          <Label for="monitor-pass">{{ t('monitor.password') }}</Label>
          <Input id="monitor-pass" v-model="config.basic_pass" type="password" />
        </div>
        <div v-if="config.auth_type === 'bearer'" class="grid gap-1 sm:col-span-2">
          <Label for="monitor-token">{{ t('monitor.token') }}</Label>
          <Input id="monitor-token" v-model="config.bearer_token" type="password" />
        </div>

        <div v-if="type === 'keyword'" class="grid gap-1 sm:col-span-2">
          <Label for="monitor-keyword" required>{{ t('monitor.keyword') }}</Label>
          <Input id="monitor-keyword" v-model="config.keyword" :placeholder="t('monitor.keywordPlaceholder')" />
        </div>
        <div v-if="type === 'keyword'" class="flex flex-wrap items-center gap-4 sm:col-span-2">
          <Switch v-model="config.invert_keyword as boolean">{{ t('monitor.invertKeyword') }}</Switch>
          <Switch v-model="config.case_sensitive as boolean">{{ t('monitor.caseSensitive') }}</Switch>
        </div>

        <div class="flex flex-wrap items-center gap-4 sm:col-span-2">
          <Switch v-model="config.ignore_tls as boolean">{{ t('monitor.ignoreTls') }}</Switch>
          <div class="grid gap-1">
            <Label for="monitor-redirects">{{ t('monitor.maxRedirects') }}</Label>
            <Input id="monitor-redirects" v-model="config.max_redirects" type="number" min="0" max="20" class="w-24" />
          </div>
        </div>
      </section>
      <!-- TCP -->
      <section v-else-if="type === 'tcp'" class="grid gap-3 border-t border-border pt-4 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="monitor-host" required>{{ t('monitor.tcpHost') }}</Label>
          <Input id="monitor-host" v-model="config.host" placeholder="db.internal" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-port" required>{{ t('monitor.tcpPort') }}</Label>
          <Input id="monitor-port" v-model="config.port" type="number" min="1" max="65535" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-send">{{ t('monitor.tcpSend') }}</Label>
          <Input id="monitor-send" v-model="config.send" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-expect">{{ t('monitor.tcpExpect') }}</Label>
          <Input id="monitor-expect" v-model="config.expect" />
        </div>
        <p class="text-[11px] text-muted-foreground sm:col-span-2">{{ t('monitor.tcpHelp') }}</p>
      </section>

      <!-- DNS -->
      <section v-else class="grid gap-3 border-t border-border pt-4 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="monitor-hostname" required>{{ t('monitor.dnsHostname') }}</Label>
          <Input id="monitor-hostname" v-model="config.hostname" placeholder="example.com" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-resolver">{{ t('monitor.dnsResolver') }}</Label>
          <Input id="monitor-resolver" v-model="config.resolver_server" placeholder="1.1.1.1" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-record">{{ t('monitor.dnsRecordType') }}</Label>
          <Select id="monitor-record" v-model="config.record_type" :options="recordTypeOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="monitor-expected">{{ t('monitor.dnsExpected') }}</Label>
          <Input id="monitor-expected" v-model="config.expected_value" />
        </div>
        <div class="sm:col-span-2">
          <Switch v-model="config.invert_check as boolean">{{ t('monitor.dnsInvert') }}</Switch>
          <p class="mt-1 text-[11px] text-muted-foreground">{{ t('monitor.dnsInvertHelp') }}</p>
        </div>
      </section>

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
    </div>

    <template #footer>
      <Button variant="outline" @click="emit('update:modelValue', false)">{{ t('common.cancel') }}</Button>
      <Button :loading="props.saving" @click="submit">{{ t('common.save') }}</Button>
    </template>
  </Dialog>
</template>

