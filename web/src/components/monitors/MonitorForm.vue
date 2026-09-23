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
import type { Monitor, MonitorConfig, MonitorPayload, MonitorType, MonitorGroup, Notification } from '@/lib/types'

const props = defineProps<{
  modelValue: boolean
  monitor: Monitor | null
  notifications: Notification[]
  groups?: MonitorGroup[]
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
    group_ids: [],
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

const runOnOptions = computed(() => [
  { value: 'all', label: t('monitor.runOnAll') },
  { value: 'primary', label: t('monitor.runOnPrimary') },
  { value: 'node', label: t('monitor.runOnSpecific') },
])

const isHttpLike = computed(() => type.value === 'http' || type.value === 'keyword')

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

