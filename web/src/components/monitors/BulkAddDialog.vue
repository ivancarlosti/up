<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, ListPlus } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Dialog from '@/components/ui/Dialog.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { useToastStore } from '@/stores/toast'
import type { BulkReport, MonitorGroup, MonitorTemplate } from '@/lib/types'

/**
 * BulkAddDialog creates one monitor per pasted line ("name,url" / "name,host"),
 * all of them from a template. The server is what parses and validates: the
 * dialog asks for a dry run while the operator types, so the preview shows the
 * real result (including the duplicates) instead of a second parser implemented
 * in the browser.
 */
const props = defineProps<{
  modelValue: boolean
  templates: MonitorTemplate[]
  groups: MonitorGroup[]
}>()

const emit = defineEmits<{ 'update:modelValue': [value: boolean]; created: [] }>()
const { t } = useI18n()
const toasts = useToastStore()

const form = reactive<{ text: string; templateId: number | null; groupId: string; active: boolean }>({
  text: '',
  templateId: null,
  // The Select reports the chosen value as a string; it is converted to the number
  // the API expects when the request is built. 0 is "no group".
  groupId: '0',
  active: true,
})

const report = ref<BulkReport | null>(null)
const busy = ref(false)
const loading = ref(false)
const error = ref('')

const templateOptions = computed(() =>
  props.templates.map((template) => ({ value: String(template.id), label: `${template.name} (${template.type})` })),
)

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    report.value = null
    error.value = ''
    form.text = form.text || `# ${t('bulk.sampleComment')}\n`
    form.templateId = form.templateId ?? props.templates[0]?.id ?? null
  },
  { immediate: true },
)

let timer: ReturnType<typeof setTimeout> | undefined

/** preview asks the server for a dry run (debounced while typing). */
function preview(): void {
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => void run(true), 400)
}

async function run(dryRun: boolean): Promise<void> {
  if (!form.text.trim()) {
    report.value = null
    return
  }
  if (!form.templateId) {
    error.value = t('bulk.templateRequired')
    return
  }
  if (dryRun) loading.value = true
  else busy.value = true
  error.value = ''
  try {
    const response = await api.bulkCreateMonitors({
      text: form.text,
      template_id: Number(form.templateId),
      // The single group of the run: 0 means "no group", so the group a template
      // may carry can never decide for the operator.
      group_id: Number(form.groupId) || 0,
      active: form.active,
      dry_run: dryRun,
    })
    report.value = response
    if (!dryRun) {
      toasts.success(t('bulk.created', { created: response.created, skipped: response.skipped, failed: response.failed }))
      emit('created')
    }
  } catch (caught) {
    error.value = translateError(caught)
  } finally {
    loading.value = false
    busy.value = false
  }
}

/** groupOptions is the single group the new monitors join, plus "no group". */
const groupOptions = computed(() => [
  { value: 0, label: t('monitor.noGroup') },
  ...props.groups.map((group) => ({ value: group.id, label: group.name })),
])

/** statusVariant colours the per row badge. */
function statusVariant(status: string): 'success' | 'secondary' | 'danger' | 'outline' {
  switch (status) {
    case 'created':
      return 'success'
    case 'dry_run':
      return 'outline'
    case 'duplicate':
      return 'secondary'
    default:
      return 'danger'
  }
}
</script>

<template>
  <Dialog
    :model-value="props.modelValue"
    :title="t('bulk.title')"
    :description="t('bulk.help')"
    wide
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="grid gap-4">
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="bulk-template" required>{{ t('bulk.template') }}</Label>
          <Select id="bulk-template" v-model="form.templateId as number" :options="templateOptions" @change="preview" />
        </div>
        <div class="flex items-end pb-1">
          <Switch v-model="form.active">{{ t('bulk.active') }}</Switch>
        </div>
      </div>

      <div class="grid gap-1">
        <Label for="bulk-text" :help="t('bulk.formatHelp')" required>{{ t('bulk.text') }}</Label>
        <Textarea
          id="bulk-text"
          v-model="form.text"
          :rows="8"
          :placeholder="t('bulk.placeholder')"
          @input="preview"
        />
      </div>

      <div v-if="props.groups.length" class="grid gap-1">
        <Label for="bulk-group">{{ t('bulk.groups') }}</Label>
        <Select id="bulk-group" v-model="form.groupId" :options="groupOptions" />
      </div>

      <p v-if="error" class="text-xs text-status-down">{{ error }}</p>

      <div v-if="report" class="grid gap-2 rounded-md border border-border p-3">
        <div class="flex flex-wrap items-center gap-2">
          <Badge variant="outline">{{ t('bulk.parsed') }}: {{ report.parsed }}</Badge>
          <Badge variant="success">{{ t('bulk.ready') }}: {{ report.created + report.dry_run }}</Badge>
          <Badge variant="secondary">{{ t('bulk.duplicates') }}: {{ report.skipped }}</Badge>
          <Badge variant="danger">{{ t('bulk.invalid') }}: {{ report.failed }}</Badge>
          <span v-if="loading" class="text-[11px] text-muted-foreground">{{ t('common.loading') }}</span>
        </div>
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('bulk.line') }}</th>
              <th>{{ t('common.name') }}</th>
              <th>{{ t('common.status') }}</th>
              <th>{{ t('bulk.reason') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in report.rows" :key="row.line">
              <td>{{ row.line }}</td>
              <td>{{ row.name || '—' }}</td>
              <td><Badge :variant="statusVariant(row.status)">{{ t(`bulk.status.${row.status}`) }}</Badge></td>
              <td class="text-[11px] text-muted-foreground">{{ row.error }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <template #footer>
      <Button variant="outline" @click="emit('update:modelValue', false)">{{ t('common.close') }}</Button>
      <Button variant="outline" :loading="loading" @click="run(true)">
        <Eye class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('bulk.preview') }}
      </Button>
      <Button :loading="busy" @click="run(false)">
        <ListPlus class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('bulk.create') }}
      </Button>
    </template>
  </Dialog>
</template>
