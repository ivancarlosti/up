<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { FileCog, Pencil, Plus, RefreshCw, Trash2 } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import MonitorConfigFields from '@/components/monitors/MonitorConfigFields.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { useToastStore } from '@/stores/toast'
import type { MonitorConfig, MonitorGroup, MonitorTemplate, MonitorTemplatePayload, MonitorType, Notification } from '@/lib/types'

/** TemplateForm mirrors a template without optional fields (the form always has them). */
type TemplateForm = {
  name: string
  description: string
  type: MonitorType
  config: MonitorConfig
  defaults: MonitorTemplatePayload['defaults'] & Record<string, unknown>
}

/**
 * AdminMonitorTemplatesView manages the reusable monitor blueprints. A template
 * is a monitor without a target: the probe options plus the defaults that the
 * bulk importer and the bulk edit apply.
 */
const { t } = useI18n()
const toasts = useToastStore()

const templates = ref<MonitorTemplate[]>([])
const notifications = ref<Notification[]>([])
const groups = ref<MonitorGroup[]>([])
const loading = ref(false)
const saving = ref(false)

const dialogOpen = ref(false)
const editing = ref<MonitorTemplate | null>(null)
const confirmOpen = ref(false)
const pendingRemoval = ref<MonitorTemplate | null>(null)

const form = reactive<TemplateForm>(blank())

function blank(): TemplateForm {
  return {
    name: '',
    description: '',
    type: 'http',
    config: { method: 'GET', encoding: 'json', auth_type: 'none', accepted_status_codes: '200-299', max_redirects: 10, record_type: 'A', resolver_server: '1.1.1.1', headers: [] },
    defaults: {
      interval_seconds: 60,
      retries: 0,
      retries_interval_seconds: 60,
      timeout_seconds: 10,
      resend_interval_seconds: 0,
      run_on: 'all',
      run_on_nodes: '',
      node_id: '',
      tags: '',
      description: '',
      notification_ids: [],
      group_ids: [],
      cert_watch: false,
      cert_notify: false,
      cert_warn_days: '',
      domain_watch: false,
      domain_notify: false,
      domain_warn_days: '',
    },
  }
}

const typeOptions = computed(() => [
  { value: 'http', label: t('monitor.typeHttp') },
  { value: 'keyword', label: t('monitor.typeKeyword') },
  { value: 'tcp', label: t('monitor.typeTcp') },
  { value: 'dns', label: t('monitor.typeDns') },
])
const runOnOptions = computed(() => [
  { value: 'all', label: t('monitor.runOnAll') },
  { value: 'primary', label: t('monitor.runOnPrimary') },
  { value: 'node', label: t('monitor.runOnSpecific') },
  { value: 'some', label: t('monitor.runOnSome') },
])

const type = computed(() => (form.type ?? 'http') as MonitorType)
const config = computed(() => form.config ?? {})

async function load(): Promise<void> {
  loading.value = true
  try {
    const [templateList, channelList, groupList] = await Promise.all([
      api.monitorTemplates(),
      api.notifications(),
      api.monitorGroups(),
    ])
    templates.value = templateList
    notifications.value = channelList
    groups.value = groupList
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  editing.value = null
  Object.assign(form, blank())
  dialogOpen.value = true
}

function openEdit(template: MonitorTemplate): void {
  editing.value = template
  const copy = JSON.parse(JSON.stringify(template)) as MonitorTemplate
  Object.assign(form, blank(), copy)
  if (!form.config) form.config = {}
  if (!form.config.headers) form.config.headers = []
  dialogOpen.value = true
}

function toggleNotification(id: number, value: boolean): void {
  const set = new Set(form.defaults.notification_ids ?? [])
  if (value) set.add(id)
  else set.delete(id)
  form.defaults.notification_ids = [...set]
}

function toggleGroup(id: number, value: boolean): void {
  const set = new Set(form.defaults.group_ids ?? [])
  if (value) set.add(id)
  else set.delete(id)
  form.defaults.group_ids = [...set]
}

async function save(): Promise<void> {
  saving.value = true
  try {
    const payload: MonitorTemplatePayload = {
      name: form.name.trim(),
      description: form.description,
      type: form.type,
      config: { ...form.config },
      defaults: {
        ...form.defaults,
        interval_seconds: Number(form.defaults.interval_seconds) || 60,
        timeout_seconds: Number(form.defaults.timeout_seconds) || 10,
        retries: Number(form.defaults.retries) || 0,
        retries_interval_seconds: Number(form.defaults.retries_interval_seconds) || 60,
        resend_interval_seconds: Number(form.defaults.resend_interval_seconds) || 0,
      },
    }
    // The probe options come from native number inputs too (they emit strings).
    if (payload.config) {
      if (payload.config.port !== undefined && String(payload.config.port) !== '') {
        payload.config.port = Number(payload.config.port) || 0
      } else {
        delete payload.config.port
      }
      if (payload.config.max_redirects !== undefined && String(payload.config.max_redirects) !== '') {
        payload.config.max_redirects = Number(payload.config.max_redirects) || 10
      } else {
        delete payload.config.max_redirects
      }
    }
    if (editing.value) await api.updateMonitorTemplate(editing.value.id, payload)
    else await api.createMonitorTemplate(payload)
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
    await api.deleteMonitorTemplate(pendingRemoval.value.id)
    confirmOpen.value = false
    toasts.success(t('common.deleted'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col gap-4">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold">{{ t('templates.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('templates.subtitle') }}</p>
      </div>
      <div class="flex items-center gap-2">
        <Button variant="outline" size="sm" :loading="loading" @click="load">
          <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('common.refresh') }}
        </Button>
        <Button size="sm" @click="openCreate">
          <Plus class="h-3.5 w-3.5" aria-hidden="true" />
          {{ t('templates.new') }}
        </Button>
      </div>
    </header>

    <EmptyState v-if="!templates.length" :title="t('templates.empty')" :description="t('templates.emptyHint')" />

    <Card v-for="template in templates" v-else :key="template.id">
      <div class="flex flex-wrap items-start justify-between gap-3">
        <div class="min-w-0">
          <h2 class="flex items-center gap-2 text-sm font-semibold">
            <FileCog class="h-4 w-4 text-muted-foreground" aria-hidden="true" />
            {{ template.name }}
            <Badge variant="secondary">{{ template.type }}</Badge>
          </h2>
          <p v-if="template.description" class="mt-1 text-[11px] text-muted-foreground">{{ template.description }}</p>
          <div class="mt-2 flex flex-wrap gap-1 text-[11px] text-muted-foreground">
            <Badge variant="outline">{{ t('common.interval') }}: {{ template.defaults?.interval_seconds }}s</Badge>
            <Badge variant="outline">{{ t('common.timeout') }}: {{ template.defaults?.timeout_seconds }}s</Badge>
            <Badge variant="outline">{{ t('monitor.runOn') }}: {{ template.defaults?.run_on }}</Badge>
            <Badge v-if="template.defaults?.tags" variant="outline">{{ template.defaults.tags }}</Badge>
          </div>
        </div>
        <div class="flex shrink-0 items-center gap-1">
          <Button variant="ghost" size="sm" :title="t('common.edit')" :aria-label="t('common.edit')" @click="openEdit(template)">
            <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
          <Button
            variant="ghost"
            size="sm"
            class="text-status-down"
            :title="t('common.delete')"
            :aria-label="t('common.delete')"
            @click="(pendingRemoval = template), (confirmOpen = true)"
          >
            <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
          </Button>
        </div>
      </div>
    </Card>

    <Dialog v-model="dialogOpen" :title="editing ? t('templates.edit') : t('templates.new')" wide>
      <div class="grid gap-5">
        <section class="grid gap-3 sm:grid-cols-2">
          <div class="grid gap-1">
            <Label for="template-name" required>{{ t('common.name') }}</Label>
            <Input id="template-name" v-model="form.name" />
          </div>
          <div class="grid gap-1">
            <Label for="template-type">{{ t('common.type') }}</Label>
            <Select id="template-type" v-model="form.type" :options="typeOptions" />
          </div>
          <div class="grid gap-1 sm:col-span-2">
            <Label for="template-description">{{ t('common.description') }}</Label>
            <Input id="template-description" v-model="form.description" />
          </div>
        </section>

        <MonitorConfigFields :type="type" :config="config" :with-target="false" />

        <section class="grid gap-3 border-t border-border pt-4 sm:grid-cols-3">
          <div class="grid gap-1">
            <Label for="template-interval" required>{{ t('common.interval') }}</Label>
            <Input id="template-interval" v-model="form.defaults!.interval_seconds" type="number" min="5" max="86400" />
          </div>
          <div class="grid gap-1">
            <Label for="template-timeout">{{ t('common.timeout') }}</Label>
            <Input id="template-timeout" v-model="form.defaults!.timeout_seconds" type="number" min="1" max="300" />
          </div>
          <div class="grid gap-1">
            <Label for="template-retries">{{ t('common.retries') }}</Label>
            <Input id="template-retries" v-model="form.defaults!.retries" type="number" min="0" max="50" />
          </div>
          <div class="grid gap-1">
            <Label for="template-retries-interval">{{ t('monitor.retriesInterval') }}</Label>
            <Input id="template-retries-interval" v-model="form.defaults!.retries_interval_seconds" type="number" min="1" />
          </div>
          <div class="grid gap-1">
            <Label for="template-resend">{{ t('monitor.resendInterval') }}</Label>
            <Input id="template-resend" v-model="form.defaults!.resend_interval_seconds" type="number" min="0" />
          </div>
          <div class="grid gap-1">
            <Label for="template-runon">{{ t('monitor.runOn') }}</Label>
            <Select id="template-runon" v-model="form.defaults!.run_on" :options="runOnOptions" />
          </div>
          <div v-if="form.defaults!.run_on === 'node'" class="grid gap-1">
            <Label for="template-node">{{ t('monitor.targetNode') }}</Label>
            <Input id="template-node" v-model="form.defaults!.node_id" placeholder="up-node-2" />
          </div>
          <div v-if="form.defaults!.run_on === 'some'" class="grid gap-1">
            <Label for="template-nodes" :help="t('monitor.runOnSomeHelp')">{{ t('monitor.targetNodes') }}</Label>
            <Input id="template-nodes" v-model="form.defaults!.run_on_nodes" placeholder="up-node-1, up-node-2" />
          </div>
          <div class="grid gap-1">
            <Label for="template-tags">{{ t('common.tags') }}</Label>
            <Input id="template-tags" v-model="form.defaults!.tags" />
          </div>
          <div class="flex items-end pb-1">
            <Switch v-model="form.defaults!.active as boolean">{{ t('common.active') }}</Switch>
          </div>
          <div class="grid gap-1 sm:col-span-3">
            <Label for="template-default-description">{{ t('templates.monitorDescription') }}</Label>
            <Textarea id="template-default-description" v-model="form.defaults!.description" :rows="2" />
          </div>
        </section>

        <section class="grid gap-2 border-t border-border pt-4">
          <Label :help="t('templates.certHelp')">{{ t('monitor.certSection') }}</Label>
          <div class="flex flex-wrap items-center gap-4">
            <Switch v-model="form.defaults.cert_watch as boolean">{{ t('monitor.certWatch') }}</Switch>
            <Switch v-model="form.defaults.cert_notify as boolean">{{ t('monitor.certNotify') }}</Switch>
          </div>
          <div v-if="form.defaults.cert_watch" class="grid gap-1 sm:max-w-sm">
            <Label for="template-cert-warn" :help="t('monitor.certWarnDaysHelp')">{{ t('monitor.certWarnDays') }}</Label>
            <Input id="template-cert-warn" v-model="form.defaults.cert_warn_days" placeholder="30,14,7,1" />
          </div>
        </section>

        <section class="grid gap-2 border-t border-border pt-4">
          <Label :help="t('templates.domainHelp')">{{ t('monitor.domainSection') }}</Label>
          <div class="flex flex-wrap items-center gap-4">
            <Switch v-model="form.defaults.domain_watch as boolean">{{ t('monitor.domainWatch') }}</Switch>
            <Switch v-model="form.defaults.domain_notify as boolean">{{ t('monitor.domainNotify') }}</Switch>
          </div>
          <div v-if="form.defaults.domain_watch" class="grid gap-1 sm:max-w-sm">
            <Label for="template-domain-warn" :help="t('monitor.domainWarnDaysHelp')">
              {{ t('monitor.domainWarnDays') }}
            </Label>
            <Input id="template-domain-warn" v-model="form.defaults.domain_warn_days" placeholder="30,14,7,1" />
          </div>
        </section>

        <section v-if="notifications.length" class="grid gap-2 border-t border-border pt-4">
          <Label>{{ t('templates.channels') }}</Label>
          <div class="flex flex-wrap gap-4">
            <Checkbox
              v-for="channel in notifications"
              :key="channel.id"
              :model-value="(form.defaults?.notification_ids ?? []).includes(channel.id)"
              @update:model-value="toggleNotification(channel.id, $event)"
            >
              {{ channel.name }}
            </Checkbox>
          </div>
        </section>

        <section v-if="groups.length" class="grid gap-2">
          <Label>{{ t('templates.groups') }}</Label>
          <div class="flex flex-wrap gap-4">
            <Checkbox
              v-for="group in groups"
              :key="group.id"
              :model-value="(form.defaults?.group_ids ?? []).includes(group.id)"
              @update:model-value="toggleGroup(group.id, $event)"
            >
              {{ group.name }}
            </Checkbox>
          </div>
        </section>
      </div>

      <template #footer>
        <Button variant="outline" @click="dialogOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="save">{{ t('common.save') }}</Button>
      </template>
    </Dialog>

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('templates.deleteTitle')"
      :description="t('templates.deleteWarning')"
      :confirm-label="t('common.delete')"
      @confirm="confirmRemove"
    />
  </div>
</template>
