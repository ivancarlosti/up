<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Bell, Pencil, Plus, Send, Trash2 } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { useToastStore } from '@/stores/toast'
import type { Notification, NotificationConfig, NotificationLog, SMTPConfig, WebhookConfig } from '@/lib/types'

const { t, locale } = useI18n()
const toasts = useToastStore()

const channels = ref<Notification[]>([])
const logs = ref<NotificationLog[]>([])
const loading = ref(false)
const dialogOpen = ref(false)
const saving = ref(false)
const testing = ref<number | null>(null)
const confirmOpen = ref(false)
const pendingRemoval = ref<Notification | null>(null)

const editing = ref<Notification | null>(null)
const form = ref<Notification>(blankChannel())

function blankSMTP(): SMTPConfig {
  return {
    host: '',
    port: 587,
    username: '',
    password: '',
    from: '',
    to: '',
    secure: false,
    use_html: true,
    skip_tls_verify: false,
    subject_prefix: '[Up]',
  }
}

function blankWebhook(): WebhookConfig {
  return { url: '', method: 'POST', content_type: 'application/json', headers: [], body_template: '' }
}

function blankChannel(): Notification {
  return {
    id: 0,
    name: '',
    type: 'smtp',
    active: true,
    is_default: false,
    resend_interval_seconds: 0,
    created_at: '',
    updated_at: '',
    monitor_ids: [],
    config: { smtp: blankSMTP() },
  }
}

/**
 * ensureConfig fills the block of the selected type. The dialog only renders the
 * fields of that block, so without it switching the type to webhook showed an
 * empty form (no URL, no method) and the save was rejected server side with
 * "config.webhook is required".
 */
function ensureConfig(): void {
  if (!form.value.config) form.value.config = {}
  if (form.value.type === 'smtp' && !form.value.config.smtp) form.value.config.smtp = blankSMTP()
  if (form.value.type === 'webhook' && !form.value.config.webhook) form.value.config.webhook = blankWebhook()
}

// The dialog must follow the type selector, not only the moment it is opened.
watch(() => form.value.type, ensureConfig, { immediate: true })

function addHeader(): void {
  const webhook = form.value.config.webhook
  if (!webhook) return
  if (!webhook.headers) webhook.headers = []
  webhook.headers.push({ key: '', value: '' })
}

function removeHeader(index: number): void {
  form.value.config.webhook?.headers?.splice(index, 1)
}

const typeOptions = computed(() => [
  { value: 'smtp', label: t('notifications.typeSmtp') },
  { value: 'webhook', label: t('notifications.typeWebhook') },
])

const methodOptions = ['POST', 'PUT', 'PATCH', 'GET', 'DELETE'].map((value) => ({ value, label: value }))

async function load(): Promise<void> {
  loading.value = true
  try {
    const [list, history] = await Promise.all([api.notifications(), api.notificationLogs({ limit: 50 })])
    channels.value = list
    logs.value = history
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    loading.value = false
  }
}

function openCreate(): void {
  editing.value = null
  form.value = blankChannel()
  dialogOpen.value = true
}

function openEdit(channel: Notification): void {
  editing.value = channel
  form.value = JSON.parse(JSON.stringify(channel))
  ensureConfig()
  dialogOpen.value = true
}

/**
 * channelPayload builds the body the API accepts. It drops the read-only fields
 * on purpose (an empty `created_at` cannot be decoded into a time.Time and the
 * API answers "invalid request body") and coerces the numbers, because a native
 * number input emits strings.
 */
function channelPayload(): Partial<Notification> {
  const channel = form.value
  const config: NotificationConfig = {}
  if (channel.type === 'smtp' && channel.config.smtp) {
    config.smtp = { ...channel.config.smtp, port: Number(channel.config.smtp.port) || 587 }
  }
  if (channel.type === 'webhook' && channel.config.webhook) {
    const webhook = channel.config.webhook
    config.webhook = {
      ...webhook,
      url: webhook.url.trim(),
      body_template: webhook.body_template ?? '',
      // A header without a name is a typo, not a header.
      headers: (webhook.headers ?? []).filter((header) => header.key.trim() !== ''),
    }
  }
  return {
    name: channel.name.trim(),
    type: channel.type,
    active: channel.active,
    is_default: channel.is_default,
    resend_interval_seconds: Number(channel.resend_interval_seconds) || 0,
    monitor_ids: channel.monitor_ids ?? [],
    config,
  }
}

async function save(): Promise<void> {
  saving.value = true
  try {
    const payload = channelPayload()
    const saved = editing.value
      ? await api.updateNotification(editing.value.id, payload)
      : await api.createNotification(payload)
    const index = channels.value.findIndex((item) => item.id === saved.id)
    if (index >= 0) channels.value.splice(index, 1, saved)
    else channels.value.push(saved)
    dialogOpen.value = false
    toasts.success(t('common.saved'))
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    saving.value = false
  }
}

async function test(channel: Notification): Promise<void> {
  testing.value = channel.id
  try {
    const entry = await api.testNotification(channel.id)
    if (entry.success) toasts.success(t('notifications.testSuccess'))
    else toasts.error(t('notifications.testFailed'), entry.error)
    logs.value = await api.notificationLogs({ limit: 50 })
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    testing.value = null
  }
}

function askRemove(channel: Notification): void {
  pendingRemoval.value = channel
  confirmOpen.value = true
}

async function confirmRemove(): Promise<void> {
  if (!pendingRemoval.value) return
  try {
    await api.deleteNotification(pendingRemoval.value.id)
    channels.value = channels.value.filter((item) => item.id !== pendingRemoval.value?.id)
    confirmOpen.value = false
    toasts.success(t('common.deleted'))
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
        <h1 class="text-lg font-semibold">{{ t('notifications.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('notifications.subtitle') }}</p>
      </div>
      <Button size="sm" @click="openCreate">
        <Plus class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('notifications.new') }}
      </Button>
    </header>

    <EmptyState
      v-if="!loading && !channels.length"
      :title="t('notifications.empty')"
      :description="t('notifications.emptyHint')"
    />

    <div v-else class="grid gap-3 lg:grid-cols-2">
      <Card v-for="channel in channels" :key="channel.id">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0">
            <h2 class="flex items-center gap-2 truncate text-sm font-semibold">
              <Bell class="h-4 w-4 text-muted-foreground" aria-hidden="true" />
              {{ channel.name }}
            </h2>
            <p class="mt-1 text-[11px] text-muted-foreground">
              {{ channel.type === 'smtp' ? channel.config.smtp?.host : channel.config.webhook?.url }}
            </p>
          </div>
          <div class="flex shrink-0 flex-col items-end gap-1">
            <Badge :variant="channel.active ? 'success' : 'secondary'">
              {{ channel.active ? t('common.enabled') : t('common.disabled') }}
            </Badge>
            <Badge variant="outline">{{ t('notifications.linkedMonitors') }}: {{ channel.monitor_ids.length }}</Badge>
          </div>
        </div>

        <template #footer>
          <Button variant="ghost" size="sm" :loading="testing === channel.id" @click="test(channel)">
            <Send class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('common.test') }}
          </Button>
          <Button variant="ghost" size="sm" @click="openEdit(channel)">
            <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('common.edit') }}
          </Button>
          <Button variant="ghost" size="sm" class="text-status-down" @click="askRemove(channel)">
            <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('common.delete') }}
          </Button>
        </template>
      </Card>
    </div>

    <Card :title="t('notifications.logs')">
      <table class="data-table">
        <thead>
          <tr>
            <th>{{ t('common.name') }}</th>
            <th>{{ t('common.status') }}</th>
            <th>{{ t('common.latency') }}</th>
            <th>event</th>
            <th>{{ t('monitorDetail.checkedAt') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="entry in logs" :key="entry.id">
            <td>#{{ entry.notification_id }} / #{{ entry.monitor_id }}</td>
            <td>
              <Badge :variant="entry.success ? 'success' : 'danger'">{{ entry.success ? 'ok' : 'error' }}</Badge>
            </td>
            <td>{{ entry.duration_ms }} ms</td>
            <td>
              <div class="max-w-[18rem] truncate" :title="entry.error">{{ entry.event }} {{ entry.error }}</div>
            </td>
            <td class="text-muted-foreground">{{ formatDateTime(entry.created_at, locale) }}</td>
          </tr>
          <tr v-if="!logs.length">
            <td class="py-2 text-muted-foreground" colspan="5">{{ t('notifications.emptyLogs') }}</td>
          </tr>
        </tbody>
      </table>
    </Card>

    <Dialog v-model="dialogOpen" :title="editing ? t('notifications.edit') : t('notifications.new')" wide>
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="channel-name" required>{{ t('common.name') }}</Label>
          <Input id="channel-name" v-model="form.name" />
        </div>
        <div class="grid gap-1">
          <Label for="channel-type">{{ t('common.type') }}</Label>
          <Select id="channel-type" v-model="form.type" :options="typeOptions" />
        </div>
        <div class="flex items-end gap-4 pb-1">
          <Switch v-model="form.active">{{ t('common.enabled') }}</Switch>
          <Switch v-model="form.is_default">{{ t('notifications.defaultChannel') }}</Switch>
        </div>
        <div class="grid gap-1">
          <Label for="channel-resend">{{ t('notifications.resendInterval') }}</Label>
          <Input id="channel-resend" v-model="form.resend_interval_seconds" type="number" min="0" />
        </div>

        <template v-if="form.type === 'smtp' && form.config.smtp">
          <div class="grid gap-1">
            <Label for="smtp-host" required>{{ t('notifications.smtpHost') }}</Label>
            <Input id="smtp-host" v-model="form.config.smtp.host" placeholder="smtp.example.com" />
          </div>
          <div class="grid gap-1">
            <Label for="smtp-port" required>{{ t('notifications.smtpPort') }}</Label>
            <Input id="smtp-port" v-model="form.config.smtp.port" type="number" min="1" max="65535" />
          </div>
          <div class="grid gap-1">
            <Label for="smtp-user">{{ t('notifications.smtpUsername') }}</Label>
            <Input id="smtp-user" v-model="form.config.smtp.username" />
          </div>
          <div class="grid gap-1">
            <Label for="smtp-pass">{{ t('notifications.smtpPassword') }}</Label>
            <Input id="smtp-pass" v-model="form.config.smtp.password" type="password" />
          </div>
          <div class="grid gap-1">
            <Label for="smtp-from" required>{{ t('notifications.smtpFrom') }}</Label>
            <Input id="smtp-from" v-model="form.config.smtp.from" type="email" />
          </div>
          <div class="grid gap-1">
            <Label for="smtp-to" required :help="t('notifications.smtpToHelp')">{{ t('notifications.smtpTo') }}</Label>
            <Input id="smtp-to" v-model="form.config.smtp.to" placeholder="ops@example.com" />
          </div>
          <div class="grid gap-1">
            <Label for="smtp-subject">{{ t('notifications.subjectPrefix') }}</Label>
            <Input id="smtp-subject" v-model="form.config.smtp.subject_prefix" />
          </div>
          <div class="flex flex-wrap items-end gap-4 pb-1">
            <Switch v-model="form.config.smtp.secure">{{ t('notifications.smtpSecure') }}</Switch>
            <Switch v-model="form.config.smtp.use_html">{{ t('notifications.smtpHtml') }}</Switch>
            <Switch v-model="form.config.smtp.skip_tls_verify">{{ t('notifications.smtpSkipTLS') }}</Switch>
          </div>
        </template>

        <template v-else-if="form.type === 'webhook' && form.config.webhook">
          <div class="grid gap-1 sm:col-span-2">
            <Label for="webhook-url" required>{{ t('notifications.webhookUrl') }}</Label>
            <Input id="webhook-url" v-model="form.config.webhook.url" placeholder="https://hooks.example.com/up" />
          </div>
          <div class="grid gap-1">
            <Label for="webhook-method">{{ t('notifications.webhookMethod') }}</Label>
            <Select id="webhook-method" v-model="form.config.webhook.method" :options="methodOptions" />
          </div>
          <div class="grid gap-1">
            <Label for="webhook-ct">{{ t('notifications.webhookContentType') }}</Label>
            <Input id="webhook-ct" v-model="form.config.webhook.content_type" placeholder="application/json" />
          </div>

          <div class="grid gap-2 sm:col-span-2">
            <div class="flex items-center justify-between gap-2">
              <Label :help="t('notifications.webhookHeadersHelp')">{{ t('notifications.webhookHeaders') }}</Label>
              <Button variant="outline" size="sm" @click="addHeader">
                <Plus class="h-3.5 w-3.5" aria-hidden="true" />
                {{ t('common.add') }}
              </Button>
            </div>
            <div
              v-for="(header, index) in form.config.webhook.headers ?? []"
              :key="index"
              class="flex items-center gap-2"
            >
              <Input v-model="header.key" :placeholder="t('notifications.headerName')" />
              <Input v-model="header.value" :placeholder="t('notifications.headerValue')" />
              <Button
                variant="ghost"
                size="icon"
                :aria-label="t('common.remove')"
                :title="t('common.remove')"
                @click="removeHeader(index)"
              >
                <Trash2 class="h-4 w-4" aria-hidden="true" />
              </Button>
            </div>
            <p v-if="!(form.config.webhook.headers ?? []).length" class="text-[11px] text-muted-foreground">
              {{ t('notifications.webhookNoHeaders') }}
            </p>
          </div>

          <div class="grid gap-1 sm:col-span-2">
            <Label for="webhook-body" :help="t('notifications.webhookBodyHelp')">
              {{ t('notifications.webhookBody') }}
            </Label>
            <Textarea id="webhook-body" v-model="form.config.webhook.body_template" :rows="8" />
          </div>
        </template>
      </div>

      <template #footer>
        <Button variant="outline" @click="dialogOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="save">{{ t('common.save') }}</Button>
      </template>
    </Dialog>

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('notifications.deleteTitle')"
      :description="t('notifications.deleteWarning')"
      :confirm-label="t('common.delete')"
      @confirm="confirmRemove"
    />
  </div>
</template>
