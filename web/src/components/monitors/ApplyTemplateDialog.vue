<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, Wand2 } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Checkbox from '@/components/ui/Checkbox.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { useToastStore } from '@/stores/toast'
import type { ApplyResult, Monitor, MonitorTemplate } from '@/lib/types'

/**
 * ApplyTemplateDialog is the bulk edit: one template, a selection of monitors and
 * the list of fields to overwrite. The preview is a dry run on the server, which
 * is also what guarantees the invariant: the target of a monitor (its URL, host
 * or hostname) is never part of what a template applies.
 */
const props = defineProps<{
  modelValue: boolean
  templates: MonitorTemplate[]
  monitors: Monitor[]
}>()

const emit = defineEmits<{ 'update:modelValue': [value: boolean]; applied: [] }>()
const { t } = useI18n()
const toasts = useToastStore()

/** Fields that are safe by default: the probe configuration and the links are opt-in. */
const SAFE_FIELDS = [
  'description',
  'interval_seconds',
  'retries',
  'retries_interval_seconds',
  'timeout_seconds',
  'resend_interval_seconds',
  'run_on',
  'node_id',
  'tags',
]

const ALL_FIELDS = [...SAFE_FIELDS, 'active', 'config', 'notification_ids', 'group_ids', 'cert_watch', 'cert_notify', 'cert_warn_days']

const form = reactive<{ templateId: number | null; fields: string[]; monitorIds: number[]; search: string }>({
  templateId: null,
  fields: [...SAFE_FIELDS],
  monitorIds: [],
  search: '',
})

const results = ref<ApplyResult[] | null>(null)
const previewing = ref(false)
const applying = ref(false)
const error = ref('')

const templateOptions = computed(() =>
  props.templates.map((template) => ({ value: String(template.id), label: `${template.name} (${template.type})` })),
)

const fieldLabels: Record<string, string> = {
  description: 'common.description',
  interval_seconds: 'common.interval',
  retries: 'common.retries',
  retries_interval_seconds: 'monitor.retriesInterval',
  timeout_seconds: 'common.timeout',
  resend_interval_seconds: 'monitor.resendInterval',
  run_on: 'monitor.runOn',
  node_id: 'monitor.targetNode',
  tags: 'common.tags',
  active: 'common.active',
  config: 'monitor.probeOptions',
  notification_ids: 'monitor.notificationsSection',
  group_ids: 'groups.title',
  cert_watch: 'monitor.certWatch',
  cert_notify: 'monitor.certNotify',
  cert_warn_days: 'monitor.certWarnDays',
}

const filteredMonitors = computed(() => {
  const term = form.search.trim().toLowerCase()
  if (!term) return props.monitors
  return props.monitors.filter((monitor) => monitor.name.toLowerCase().includes(term))
})

const changedCount = computed(() => (results.value ?? []).filter((result) => (result.changed ?? []).length > 0).length)

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    results.value = null
    error.value = ''
    form.fields = [...SAFE_FIELDS]
    form.templateId = form.templateId ?? props.templates[0]?.id ?? null
  },
  { immediate: true },
)

function toggleField(field: string, value: boolean): void {
  const set = new Set(form.fields)
  if (value) set.add(field)
  else set.delete(field)
  // Keep the canonical order so the diff reads predictably.
  form.fields = ALL_FIELDS.filter((candidate) => set.has(candidate))
  results.value = null
}

function toggleMonitor(id: number, value: boolean): void {
  const set = new Set(form.monitorIds)
  if (value) set.add(id)
  else set.delete(id)
  form.monitorIds = [...set]
  results.value = null
}

function selectAllMonitors(): void {
  form.monitorIds = filteredMonitors.value.map((monitor) => monitor.id)
  results.value = null
}

async function run(dryRun: boolean): Promise<void> {
  if (!form.templateId) {
    error.value = t('apply.templateRequired')
    return
  }
  if (!form.monitorIds.length) {
    error.value = t('apply.monitorsRequired')
    return
  }
  if (dryRun) previewing.value = true
  else applying.value = true
  error.value = ''
  try {
    const response = await api.applyMonitorTemplate(Number(form.templateId), {
      monitor_ids: form.monitorIds,
      fields: form.fields,
      dry_run: dryRun,
    })
    results.value = response.results
    if (!dryRun) {
      toasts.success(t('apply.applied', { count: response.results.filter((result) => result.applied).length }))
      emit('applied')
    }
  } catch (caught) {
    error.value = translateError(caught)
  } finally {
    previewing.value = false
    applying.value = false
  }
}
</script>

<template>
  <Dialog
    :model-value="props.modelValue"
    :title="t('apply.title')"
    :description="t('apply.help')"
    wide
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="grid gap-4">
      <div class="grid gap-1 sm:max-w-sm">
        <Label for="apply-template" required>{{ t('apply.template') }}</Label>
        <Select id="apply-template" v-model="form.templateId as number" :options="templateOptions" @change="results = null" />
      </div>

      <div class="grid gap-2">
        <Label :help="t('apply.fieldsHelp')">{{ t('apply.fields') }}</Label>
        <div class="flex flex-wrap gap-4">
          <Checkbox
            v-for="field in ALL_FIELDS"
            :key="field"
            :model-value="form.fields.includes(field)"
            @update:model-value="toggleField(field, $event)"
          >
            {{ t(fieldLabels[field]) }}
          </Checkbox>
        </div>
        <p class="text-[11px] text-muted-foreground">{{ t('apply.configNotice') }}</p>
      </div>

      <div class="grid gap-2">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <Label :help="t('apply.monitorsHelp')">{{ t('apply.monitors') }}</Label>
          <div class="flex items-center gap-2">
            <Input v-model="form.search" class="max-w-xs" :placeholder="t('common.search')" />
            <Button variant="outline" size="sm" @click="selectAllMonitors">{{ t('apply.selectAll') }}</Button>
          </div>
        </div>
        <div class="flex max-h-48 flex-wrap gap-4 overflow-y-auto rounded-md border border-border p-3">
          <Checkbox
            v-for="monitor in filteredMonitors"
            :key="monitor.id"
            :model-value="form.monitorIds.includes(monitor.id)"
            @update:model-value="toggleMonitor(monitor.id, $event)"
          >
            {{ monitor.name }}
          </Checkbox>
        </div>
        <span class="text-[11px] text-muted-foreground">
          {{ t('apply.selected', { count: form.monitorIds.length }) }}
        </span>
      </div>

      <p v-if="error" class="text-xs text-status-down">{{ error }}</p>

      <div v-if="results" class="grid gap-2 rounded-md border border-border p-3">
        <div class="flex flex-wrap items-center gap-2">
          <Badge variant="outline">{{ t('apply.analysed') }}: {{ results.length }}</Badge>
          <Badge :variant="changedCount ? 'success' : 'secondary'">{{ t('apply.wouldChange') }}: {{ changedCount }}</Badge>
        </div>
        <div v-for="result in results" :key="result.monitor_id" class="grid gap-1 border-t border-border pt-2">
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-sm font-medium">{{ result.name }}</span>
            <Badge v-if="result.error" variant="danger">{{ result.error }}</Badge>
            <Badge v-else-if="!(result.changed ?? []).length" variant="secondary">{{ t('apply.noChange') }}</Badge>
            <Badge v-else-if="result.applied" variant="success">{{ t('apply.appliedBadge') }}</Badge>
          </div>
          <ul class="grid gap-0.5 text-[11px] text-muted-foreground">
            <li v-for="change in result.changed ?? []" :key="change.field">
              {{ change.field }}: <code>{{ change.from || '—' }}</code> → <code>{{ change.to || '—' }}</code>
            </li>
          </ul>
        </div>
      </div>
    </div>

    <template #footer>
      <Button variant="outline" @click="emit('update:modelValue', false)">{{ t('common.cancel') }}</Button>
      <Button variant="outline" :loading="previewing" @click="run(true)">
        <Eye class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('apply.preview') }}
      </Button>
      <Button :loading="applying" @click="run(false)">
        <Wand2 class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('apply.apply') }}
      </Button>
    </template>
  </Dialog>
</template>
