<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Eye, Tags } from 'lucide-vue-next'
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
import type { BulkTagOptions, BulkTagReport, Monitor, MonitorGroup, TagUsage } from '@/lib/types'

/**
 * BulkTagsDialog adds and removes tags on many monitors at once.
 *
 * The tags are a free form list on the monitor, so the operations worth a dialog
 * are the ones no form can do one monitor at a time: retagging everything that
 * carries a tag (a rename) and tagging everything the current filter shows. The
 * selection is sent as it is (the ids on screen, the groups, the tag) and the
 * server resolves it once, so the preview the operator confirms is exactly the set
 * of rows the run touches.
 *
 * The preview is the dry run of the very same request: the dialog never guesses
 * what "prod" matches, it asks (whole tag, case insensitive: "production" is not
 * "prod").
 */
const props = defineProps<{
  modelValue: boolean
  /** The monitors currently on screen: what "the monitors shown" means. */
  monitors: Monitor[]
  groups: MonitorGroup[]
}>()

const emit = defineEmits<{ 'update:modelValue': [value: boolean]; updated: [] }>()
const { t } = useI18n()
const toasts = useToastStore()

/** The three selections the dialog offers, in the order of the selector. */
type Selection = 'visible' | 'groups' | 'tag'

/** Maximum rows of the preview table: a 2k dashboard would render 2k rows twice. */
const maxPreviewRows = 200

const selection = ref<Selection>('visible')
const form = reactive<{ groupIds: number[]; tag: string; add: string; remove: string }>({
  groupIds: [],
  tag: '',
  add: '',
  remove: '',
})

const report = ref<BulkTagReport | null>(null)
const tagUsage = ref<TagUsage[]>([])
const busy = ref(false)
const loading = ref(false)
const error = ref('')

const selectionOptions = computed(() => [
  { value: 'visible', label: t('bulkTags.selectionVisible', { count: props.monitors.length }) },
  { value: 'groups', label: t('bulkTags.selectionGroups') },
  { value: 'tag', label: t('bulkTags.selectionTag') },
])

const tagOptions = computed(() => [
  { value: '', label: t('bulkTags.tagPlaceholder') },
  ...tagUsage.value.map((item) => ({ value: item.tag, label: `${item.tag} (${item.monitors})` })),
])

/** previewRows caps what the table renders, with the remainder counted below. */
const previewRows = computed(() => (report.value?.changes ?? []).slice(0, maxPreviewRows))
const hiddenRows = computed(() => Math.max(0, (report.value?.changes.length ?? 0) - maxPreviewRows))

watch(
  () => props.modelValue,
  (open) => {
    if (!open) return
    report.value = null
    error.value = ''
    if (!props.groups.length) selection.value = 'visible'
    void loadTagUsage()
    preview()
  },
  { immediate: true },
)

/**
 * loadTagUsage fills the tag selector with the tags in use. A failure is not
 * fatal: the operator can still type the tags below, only the suggestions go.
 */
async function loadTagUsage(): Promise<void> {
  try {
    tagUsage.value = await api.monitorTags()
  } catch {
    tagUsage.value = []
  }
}

/** splitTags mirrors the server side split: comma, semicolon or space separated. */
function splitTags(raw: string): string[] {
  return raw
    .split(/[,;\s]+/)
    .map((tag) => tag.trim())
    .filter((tag) => tag !== '')
}

/** selectionPayload renders the selection as the API options. */
function selectionPayload(): BulkTagOptions {
  if (selection.value === 'groups') return { group_ids: form.groupIds }
  if (selection.value === 'tag') return { tag: form.tag }
  return { monitor_ids: props.monitors.map((monitor) => monitor.id) }
}

/** selectionProblem explains what the selection is still missing, if anything. */
const selectionProblem = computed(() => {
  if (selection.value === 'groups' && !form.groupIds.length) return t('bulkTags.requiredGroups')
  if (selection.value === 'tag' && !form.tag) return t('bulkTags.requiredTag')
  if (selection.value === 'visible' && !props.monitors.length) return t('bulkTags.requiredMonitors')
  return ''
})

/** tagsProblem is the other half: a run without a tag to apply is a no-op. */
const tagsProblem = computed(() =>
  splitTags(form.add).length || splitTags(form.remove).length ? '' : t('bulkTags.requiredTags'),
)

let timer: ReturnType<typeof setTimeout> | undefined

/** preview asks the server for a dry run (debounced while typing). */
function preview(): void {
  if (timer) clearTimeout(timer)
  timer = setTimeout(() => void run(true), 400)
}

async function run(dryRun: boolean): Promise<void> {
  if (selectionProblem.value || tagsProblem.value) {
    report.value = null
    error.value = selectionProblem.value || tagsProblem.value
    return
  }
  if (dryRun) loading.value = true
  else busy.value = true
  error.value = ''
  try {
    const response = await api.bulkUpdateMonitorTags({
      ...selectionPayload(),
      add: splitTags(form.add),
      remove: splitTags(form.remove),
      dry_run: dryRun,
    })
    report.value = response
    if (!dryRun) {
      toasts.success(t('bulkTags.applied', { updated: response.updated, unchanged: response.unchanged }))
      emit('updated')
    }
  } catch (caught) {
    error.value = translateError(caught)
  } finally {
    loading.value = false
    busy.value = false
  }
}

function toggleGroup(id: number, value: boolean): void {
  const set = new Set(form.groupIds)
  if (value) set.add(id)
  else set.delete(id)
  form.groupIds = [...set]
  preview()
}

/** statusVariant colours a row of the report like the bulk creation report does. */
function statusVariant(status: string): 'outline' | 'success' | 'danger' {
  if (status === 'updated') return 'success'
  if (status === 'failed') return 'danger'
  return 'outline'
}
</script>

<template>
  <Dialog
    :model-value="props.modelValue"
    :title="t('bulkTags.title')"
    :description="t('bulkTags.help')"
    wide
    @update:model-value="emit('update:modelValue', $event)"
  >
    <div class="grid gap-4">
      <div class="grid gap-1">
        <Label for="bulk-tags-selection">{{ t('bulkTags.selection') }}</Label>
        <Select id="bulk-tags-selection" v-model="selection as string" :options="selectionOptions" @change="preview" />
      </div>

      <div v-if="selection === 'groups'" class="grid gap-2 rounded-md border border-border p-3">
        <p v-if="!props.groups.length" class="text-[11px] text-muted-foreground">{{ t('bulkTags.noGroups') }}</p>
        <div v-else class="flex flex-wrap gap-4">
          <Checkbox
            v-for="group in props.groups"
            :key="group.id"
            :model-value="form.groupIds.includes(group.id)"
            @update:model-value="toggleGroup(group.id, $event)"
          >
            {{ group.name }} ({{ group.monitor_count }})
          </Checkbox>
        </div>
      </div>

      <div v-if="selection === 'tag'" class="grid gap-1">
        <Label for="bulk-tags-tag">{{ t('bulkTags.tag') }}</Label>
        <Select id="bulk-tags-tag" v-model="form.tag" :options="tagOptions" @change="preview" />
      </div>

      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="bulk-tags-add" :help="t('bulkTags.tagHint')">{{ t('bulkTags.add') }}</Label>
          <Input id="bulk-tags-add" v-model="form.add" :placeholder="t('monitor.tagsPlaceholder')" @input="preview" />
        </div>
        <div class="grid gap-1">
          <Label for="bulk-tags-remove">{{ t('bulkTags.remove') }}</Label>
          <Input id="bulk-tags-remove" v-model="form.remove" @input="preview" />
        </div>
      </div>

      <p v-if="error" class="text-xs text-status-down">{{ error }}</p>

      <div v-if="report" class="grid gap-2 rounded-md border border-border p-3">
        <div class="flex flex-wrap items-center gap-2">
          <Badge variant="outline">{{ t('bulkTags.selected') }}: {{ report.selected }}</Badge>
          <Badge variant="success">{{ t('bulkTags.updated') }}: {{ report.updated }}</Badge>
          <Badge variant="secondary">{{ t('bulkTags.unchanged') }}: {{ report.unchanged }}</Badge>
          <Badge variant="danger">{{ t('bulkTags.failed') }}: {{ report.failed }}</Badge>
          <span v-if="loading" class="text-[11px] text-muted-foreground">{{ t('common.loading') }}</span>
        </div>
        <p v-if="!report.changes.length" class="text-[11px] text-muted-foreground">
          {{ report.selected ? t('bulkTags.nothingToDo') : t('bulkTags.emptySelection') }}
        </p>
        <table v-else class="data-table">
          <thead>
            <tr>
              <th>{{ t('common.name') }}</th>
              <th>{{ t('bulkTags.before') }}</th>
              <th>{{ t('bulkTags.after') }}</th>
              <th>{{ t('common.status') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in previewRows" :key="row.monitor_id">
              <td>{{ row.name }}</td>
              <td class="text-[11px] text-muted-foreground">{{ row.tags_before || '—' }}</td>
              <td class="text-[11px]">{{ row.tags_after || '—' }}</td>
              <td>
                <Badge :variant="statusVariant(row.status)">{{ t(`bulkTags.status.${row.status}`) }}</Badge>
                <span v-if="row.error" class="block text-[11px] text-muted-foreground">{{ row.error }}</span>
              </td>
            </tr>
          </tbody>
        </table>
        <p v-if="hiddenRows" class="text-[11px] text-muted-foreground">
          {{ t('bulkTags.more', { count: hiddenRows }) }}
        </p>
      </div>
    </div>

    <template #footer>
      <Button variant="outline" @click="emit('update:modelValue', false)">{{ t('common.close') }}</Button>
      <Button variant="outline" :loading="loading" @click="run(true)">
        <Eye class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('bulkTags.preview') }}
      </Button>
      <Button :loading="busy" :disabled="Boolean(selectionProblem || tagsProblem)" @click="run(false)">
        <Tags class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('bulkTags.apply') }}
      </Button>
    </template>
  </Dialog>
</template>
