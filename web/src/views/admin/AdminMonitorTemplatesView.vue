<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { FileCog, Link2, Pencil, Plus, RefreshCw, RotateCw, Trash2 } from 'lucide-vue-next'
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
import SortHeader from '@/components/ui/SortHeader.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import MonitorConfigFields from '@/components/monitors/MonitorConfigFields.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import {
  pruneMonitorConfig,
  sanitizeTemplateDefaults,
  stripMonitorAuth,
  supportsCertificate as typeSupportsCertificate,
  supportsDomainWatch as typeSupportsDomainWatch,
} from '@/lib/monitor-config'
import { monitorTemplateSortKeys, sortMonitorTemplates } from '@/lib/sort'
import type { MonitorTemplateSortKey } from '@/lib/sort'
import { loadTableSort, toggleTableSort } from '@/lib/table-sort'
import { useToastStore } from '@/stores/toast'
import type {
  MonitorConfig,
  MonitorGroup,
  MonitorTemplate,
  MonitorTemplatePayload,
  MonitorType,
  Notification,
  TagUsage,
  TemplateLinkResult,
  TemplateLinkScope,
  TemplateLinkScopeKind,
} from '@/lib/types'

/** TemplateForm mirrors a template without optional fields (the form always has them). */
type TemplateForm = {
  name: string
  description: string
  type: MonitorType
  config: MonitorConfig
  defaults: MonitorTemplatePayload['defaults'] & Record<string, unknown>
  propagate: boolean
}

/**
 * AdminMonitorTemplatesView manages the reusable monitor blueprints. A template
 * is a monitor without a target: the probe options plus the defaults that the
 * bulk importer and the bulk edit apply.
 */
const { t, locale } = useI18n()
const toasts = useToastStore()

const templates = ref<MonitorTemplate[]>([])
const notifications = ref<Notification[]>([])
const groups = ref<MonitorGroup[]>([])
const loading = ref(false)
const saving = ref(false)

// Filter and sort of the templates table: the text filter looks at the name and
// the description, the type filter at the probe type, and the sort choice is
// remembered per browser (lib/table-sort.ts).
const templateSearch = ref('')
const templateType = ref<'' | MonitorType>('')
const TEMPLATE_SORT_STORAGE_KEY = 'up.admin.monitor-templates.sort'
const sort = ref(
  loadTableSort({
    storageKey: TEMPLATE_SORT_STORAGE_KEY,
    keys: monitorTemplateSortKeys,
    defaultKey: 'name',
  }),
)

/** toggleSort switches the column, or flips the direction of the current one. */
function toggleSort(key: MonitorTemplateSortKey): void {
  sort.value = toggleTableSort(sort.value, key, TEMPLATE_SORT_STORAGE_KEY)
}

/** filteredTemplates applies both filters of the table. */
const filteredTemplates = computed(() => {
  const term = templateSearch.value.trim().toLowerCase()
  return templates.value.filter((template) => {
    if (templateType.value && template.type !== templateType.value) return false
    if (!term) return true
    return [template.name, template.description].some((value) => (value ?? '').toLowerCase().includes(term))
  })
})

/** sortedTemplates applies the chosen column to the filtered rows (see lib/sort.ts). */
const sortedTemplates = computed(() =>
  sortMonitorTemplates(filteredTemplates.value, sort.value.key, sort.value.direction, { locale: locale.value }),
)

const dialogOpen = ref(false)
const editing = ref<MonitorTemplate | null>(null)
const confirmOpen = ref(false)
const pendingRemoval = ref<MonitorTemplate | null>(null)

// "Link monitors to this template": a dry run fills the preview the
// confirmation dialog shows, the real run writes the link and the defaults.
//
// The scope is the decision the dialog exists for, and it is REMEMBERED on the
// template (MonitorTemplate.link_scope), so re-opening the dialog shows the
// selection of the last run instead of quietly falling back to "every monitor of
// this type". The selection is held as the scope itself (group uuids, tags): the
// uuids are what the API stores, because the ids of the groups differ from one
// node of a cluster to the next.
const linkOpen = ref(false)
const linking = ref(false)
const linkPreviewing = ref(false)
const linkTarget = ref<MonitorTemplate | null>(null)
const linkPreview = ref<TemplateLinkResult | null>(null)
const linkScope = ref<TemplateLinkScopeKind>('type')
const linkGroupUUIDs = ref<string[]>([])
const linkTags = ref<string[]>([])
/** The tag input of the dialog: a tag no monitor carries yet can still be selected. */
const linkTagInput = ref('')
/** The tags in use among the monitors of the template type (the picker's vocabulary). */
const linkTagUsage = ref<TagUsage[]>([])
/** The template whose stored scope is being re-run (the row action shows a spinner). */
const reapplyingID = ref<number | null>(null)

const form = reactive<TemplateForm>(blank())

function blank(): TemplateForm {
  return {
    name: '',
    description: '',
    type: 'http',
    propagate: true,
    config: { method: 'GET', encoding: 'json', auth_type: 'none', accepted_status_codes: '200-299', max_redirects: 10, record_type: 'A', resolver_server: '1.1.1.1', ip_family: 'auto', headers: [] },
    defaults: {
      interval_seconds: 60,
      retries: 0,
      retries_interval_seconds: 60,
      timeout_seconds: 10,
      resend_interval_seconds: 0,
      run_on: 'all',
      run_on_nodes: '',
      node_id: '',
      active: true,
      description: '',
      notification_ids: [],
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
  { value: 'ssl', label: t('monitor.typeSsl') },
])

/** typeFilterOptions is the type selector of the table (the dialog has one per type). */
const typeFilterOptions = computed(() => [{ value: '', label: t('templates.allTypes') }, ...typeOptions.value])
const runOnOptions = computed(() => [
  { value: 'all', label: t('monitor.runOnAll') },
  { value: 'primary', label: t('monitor.runOnPrimary') },
  { value: 'node', label: t('monitor.runOnSpecific') },
  { value: 'some', label: t('monitor.runOnSome') },
])

/**
 * linkScopeCards are the three kinds of scope the dialog offers, as radio cards:
 * a Select of two options hid the fact that the choice decides who follows the
 * template (and who stops following it), so the decision is spelled out.
 */
const linkScopeCards = computed(() => [
  {
    kind: 'type' as TemplateLinkScopeKind,
    title: t('templates.linkScopeAll'),
    description: t('templates.linkScopeAllHelp', { type: linkTarget.value?.type ?? '' }),
  },
  {
    kind: 'groups' as TemplateLinkScopeKind,
    title: t('templates.linkScopeGroups'),
    description: t('templates.linkScopeGroupsHelp'),
  },
  {
    kind: 'tags' as TemplateLinkScopeKind,
    title: t('templates.linkScopeTags'),
    description: t('templates.linkScopeTagsHelp'),
  },
])

/**
 * linkScopeNeedsSelection is true when the selected kind has nothing selected yet:
 * there is nothing to preview and nothing to run, so the dialog asks for the
 * selection instead of showing a misleading count.
 */
const linkScopeNeedsSelection = computed(
  () =>
    (linkScope.value === 'groups' && linkGroupUUIDs.value.length === 0) ||
    (linkScope.value === 'tags' && linkTags.value.length === 0),
)

/**
 * linkReleasesFollowers is true when the scope the operator selected replaces the
 * scope of the template, so the monitors that follow it from outside the
 * selection stop following it. The dialog words the preview differently in that
 * case because the run is not only additive.
 */
const linkReleasesFollowers = computed(() => linkScope.value !== 'type')

/** linkWarning words the description of the link dialog for the selected kind. */
const linkWarning = computed(() => {
  if (linkScope.value === 'tags') return t('templates.linkAllWarningTags')
  if (linkScope.value === 'groups') return t('templates.linkAllWarningGroups')
  return t('templates.linkAllWarning')
})

/**
 * linkTagChoices is the tag vocabulary of the picker: the tags in use among the
 * monitors of the template type, plus the ones the operator typed, so a tag can
 * be selected before any monitor carries it.
 */
const linkTagChoices = computed(() => {
  const usage = new Map(linkTagUsage.value.map((item) => [item.tag.toLowerCase(), item]))
  // `monitors: null` marks a tag an operator typed that no monitor carries yet.
  const choices: { tag: string; monitors: number | null }[] = linkTagUsage.value.map((item) => ({
    tag: item.tag,
    monitors: item.monitors,
  }))
  for (const tag of linkTags.value) {
    if (!usage.has(tag.toLowerCase())) choices.push({ tag, monitors: null })
  }
  return choices
})

/** linkGroupLabel resolves a stored uuid to the group name the operator knows. */
function linkGroupLabel(uuid: string): string {
  const group = groups.value.find((candidate) => candidate.uuid === uuid)
  return group ? group.name : uuid.slice(0, 8)
}

/**
 * scopeLabel words the stored scope of a template for the table: what the last
 * link run decided to govern, which is what the row action re-applies.
 */
function scopeLabel(scope: TemplateLinkScope | undefined): string {
  if (!scope || scope.kind === 'type') return t('templates.linkScopeAll')
  if (scope.kind === 'groups') {
    const names = (scope.group_uuids ?? []).map((uuid) => linkGroupLabel(uuid))
    return `${t('templates.linkScopeGroups')}: ${names.join(', ')}`
  }
  return `${t('templates.linkScopeTags')}: ${(scope.tags ?? []).join(', ')}`
}

/** scopeHasSelection reports whether a stored scope names anything. */
function scopeHasSelection(scope: TemplateLinkScope | undefined): boolean {
  return Boolean(scope && scope.kind !== 'type')
}

const type = computed(() => (form.type ?? 'http') as MonitorType)
const config = computed(() => form.config ?? {})

/**
 * The capability predicates live in `@/lib/monitor-config`, shared with the
 * monitor form (and with the backend, `models.MonitorType`): the template dialog
 * must not offer a switch the selected probe type cannot honour.
 */
const supportsCertificate = computed(() => typeSupportsCertificate(type.value))
const supportsDomain = computed(() => typeSupportsDomainWatch(type.value))

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

async function save(): Promise<void> {
  saving.value = true
  try {
    const payload: MonitorTemplatePayload = {
      name: form.name.trim(),
      description: form.description,
      type: form.type,
      propagate: Boolean(form.propagate),
      config: stripMonitorAuth(pruneMonitorConfig(form.type, form.config)),
      // The switches of the selected type only: the dialog keeps the values of
      // the type the operator experimented with hidden (like the monitor form
      // does), a certificate watch on a tcp template would be stored here and
      // then rejected by the API, and every monitor created from it too.
      defaults: sanitizeTemplateDefaults(form.type, {
        ...form.defaults,
        interval_seconds: Number(form.defaults.interval_seconds) || 60,
        timeout_seconds: Number(form.defaults.timeout_seconds) || 10,
        retries: Number(form.defaults.retries) || 0,
        retries_interval_seconds: Number(form.defaults.retries_interval_seconds) || 60,
        resend_interval_seconds: Number(form.defaults.resend_interval_seconds) || 0,
      }),
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

/**
 * openLink opens the link dialog seeded with the STORED scope of the template,
 * which is the whole point of persisting it: the dialog answers "what does this
 * template govern?" with the decision of the last run, and the operator only has
 * to change what changed.
 */
async function openLink(template: MonitorTemplate): Promise<void> {
  linkTarget.value = template
  linkPreview.value = null
  const scope = template.link_scope
  linkScope.value = scope?.kind ? scope.kind : 'type'
  // A group deleted elsewhere (another node, another admin) is dropped instead of
  // staying selected invisibly: the backend prunes the same reference, and the
  // operator must see the selection they can actually see.
  const knownGroups = new Set(groups.value.map((group) => group.uuid))
  linkGroupUUIDs.value = (scope?.group_uuids ?? []).filter((uuid) => knownGroups.has(uuid))
  linkTags.value = [...(scope?.tags ?? [])]
  linkTagInput.value = ''
  // The picker offers the tags the monitors of this type actually carry: a tag
  // nobody uses can still be typed, but the list starts from reality.
  linkTagUsage.value = []
  void loadLinkTags(template.type)
  linkOpen.value = true
  await refreshLinkPreview()
}

/**
 * loadLinkTags fills the tag vocabulary of the picker. A failure is not fatal:
 * the operator can still type the tags, so the dialog only loses its suggestions.
 */
async function loadLinkTags(type: MonitorType | undefined): Promise<void> {
  try {
    linkTagUsage.value = await api.monitorTags(type)
  } catch {
    linkTagUsage.value = []
  }
}

/** buildLinkScope renders the selection of the dialog as the API scope. */
function buildLinkScope(): TemplateLinkScope {
  if (linkScope.value === 'groups') return { kind: 'groups', group_uuids: [...linkGroupUUIDs.value] }
  if (linkScope.value === 'tags') return { kind: 'tags', tags: [...linkTags.value] }
  return { kind: 'type' }
}

/**
 * refreshLinkPreview re-runs the dry run for the selected scope. With a kind that
 * has nothing selected yet there is nothing to preview: the dialog asks for the
 * selection instead of showing a misleading count.
 */
async function refreshLinkPreview(): Promise<void> {
  if (!linkTarget.value) return
  if (linkScopeNeedsSelection.value) {
    linkPreview.value = null
    return
  }
  linkPreviewing.value = true
  linkPreview.value = null
  try {
    linkPreview.value = await api.linkAllMonitorTemplate(linkTarget.value.id, true, buildLinkScope())
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
    linkOpen.value = false
  } finally {
    linkPreviewing.value = false
  }
}

function setLinkScope(kind: TemplateLinkScopeKind): void {
  linkScope.value = kind
  void refreshLinkPreview()
}

function toggleLinkGroup(uuid: string, value: boolean): void {
  const set = new Set(linkGroupUUIDs.value)
  if (value) set.add(uuid)
  else set.delete(uuid)
  linkGroupUUIDs.value = [...set]
  void refreshLinkPreview()
}

function toggleLinkTag(tag: string, value: boolean): void {
  const set = new Set(linkTags.value.map((candidate) => candidate.toLowerCase()))
  if (value) set.add(tag.toLowerCase())
  else set.delete(tag.toLowerCase())
  // The selected tags are kept as they were spelled by the monitor that carries
  // them (or as the operator typed them): the comparison is case insensitive, the
  // stored value is not rewritten.
  linkTags.value = [...set].map((key) => linkTagChoices.value.find((choice) => choice.tag.toLowerCase() === key)?.tag ?? key)
  void refreshLinkPreview()
}

/** addLinkTag selects a tag that is not in the vocabulary yet. */
function addLinkTag(): void {
  const tag = linkTagInput.value.trim()
  linkTagInput.value = ''
  if (!tag) return
  if (linkTags.value.some((candidate) => candidate.toLowerCase() === tag.toLowerCase())) return
  linkTags.value = [...linkTags.value, tag]
  void refreshLinkPreview()
}

async function confirmLink(): Promise<void> {
  if (!linkTarget.value || linkScopeNeedsSelection.value) return
  linking.value = true
  // The scope is captured before the request: the toast must word the run the way
  // the dialog did, even if the operator switches the selector afterwards.
  const scope = buildLinkScope()
  try {
    const result = await api.linkAllMonitorTemplate(linkTarget.value.id, false, scope)
    toasts.success(
      scope.kind === 'type'
        ? t('templates.linkAllDone', { linked: result.linked, updated: result.updated })
        : t('templates.linkAllDoneGroups', {
            linked: result.linked,
            updated: result.updated,
            unlinked: result.unlinked,
          }),
    )
    linkOpen.value = false
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    linking.value = false
  }
}

/**
 * reapplyLink opens the dialog on the stored scope of a template, which is the
 * "run it again" shortcut: the preview and the detach warning are the ones of an
 * ordinary run, so the operator confirms what the template is already supposed to
 * govern instead of trusting a silent re-run.
 */
async function reapplyLink(template: MonitorTemplate): Promise<void> {
  reapplyingID.value = template.id
  try {
    await openLink(template)
  } finally {
    reapplyingID.value = null
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

    <Card v-else :padded="false">
      <div class="flex flex-wrap items-center gap-2 border-b border-border px-5 py-4">
        <Input v-model="templateSearch" class="max-w-xs" :placeholder="t('templates.filterPlaceholder')" />
        <Select v-model="templateType" class="w-40" :options="typeFilterOptions" />
        <span class="text-[11px] text-muted-foreground">
          {{ t('common.shownOfTotal', { shown: filteredTemplates.length, total: templates.length }) }}
        </span>
      </div>

      <p v-if="!filteredTemplates.length" class="p-5 text-xs text-muted-foreground">
        {{ t('common.noMatch') }}
      </p>

      <div v-else class="overflow-x-auto">
        <table class="data-table">
          <thead>
            <tr>
              <SortHeader
                :label="t('common.name')"
                column="name"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('name')"
              />
              <SortHeader
                :label="t('common.type')"
                column="type"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('type')"
              />
              <SortHeader
                :label="t('templates.monitors')"
                column="monitors"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('monitors')"
              />
              <th>{{ t('templates.linkScopeColumn') }}</th>
              <SortHeader
                :label="t('common.interval')"
                column="interval"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('interval')"
              />
              <th class="text-end">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="template in sortedTemplates" :key="template.id">
              <td>
                <span class="flex items-center gap-2 text-sm font-medium">
                  <FileCog class="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                  {{ template.name }}
                </span>
                <span v-if="template.description" class="block text-[11px] text-muted-foreground">
                  {{ template.description }}
                </span>
                <span class="mt-1 flex flex-wrap gap-1">
                  <Badge v-if="typeSupportsCertificate(template.type) && template.defaults?.cert_watch" variant="outline">
                    {{ t('monitor.certWatch') }}
                  </Badge>
                  <Badge v-if="typeSupportsDomainWatch(template.type) && template.defaults?.domain_watch" variant="outline">
                    {{ t('monitor.domainWatch') }}
                  </Badge>
                </span>
              </td>
              <td>
                <Badge variant="secondary">{{ template.type }}</Badge>
              </td>
              <td class="whitespace-nowrap tabular-nums">{{ template.monitor_count }}</td>
              <!--
                What the last link run decided to govern. It is shown because the
                scope survives the dialog: an admin who cannot see it cannot tell
                a template that governs everything from one that governs a group.
              -->
              <td>
                <Badge :variant="scopeHasSelection(template.link_scope) ? 'secondary' : 'outline'">
                  {{ scopeLabel(template.link_scope) }}
                </Badge>
              </td>
              <td class="whitespace-nowrap">
                <span class="tabular-nums">{{ template.defaults?.interval_seconds }}s</span>
                <span class="block text-[11px] text-muted-foreground">
                  {{ t('common.timeout') }} {{ template.defaults?.timeout_seconds }}s ·
                  {{ t('monitor.runOn') }} {{ template.defaults?.run_on }}
                </span>
              </td>
              <td class="text-end">
                <div class="flex items-center justify-end gap-1">
                  <!--
                    Re-run the link with the scope of the last run. It opens the
                    dialog on the stored selection instead of writing silently:
                    a scope with an outside detaches followers, so the operator
                    confirms the preview exactly like a hand-made run.
                  -->
                  <Button
                    v-if="scopeHasSelection(template.link_scope)"
                    variant="ghost"
                    size="sm"
                    :loading="reapplyingID === template.id"
                    :title="t('templates.linkReapply')"
                    :aria-label="t('templates.linkReapply')"
                    @click="reapplyLink(template)"
                  >
                    <RotateCw class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    :title="t('templates.linkAll')"
                    :aria-label="t('templates.linkAll')"
                    @click="openLink(template)"
                  >
                    <Link2 class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    :title="t('common.edit')"
                    :aria-label="t('common.edit')"
                    @click="openEdit(template)"
                  >
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
              </td>
            </tr>
          </tbody>
        </table>
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

        <MonitorConfigFields :type="type" :config="config" :with-target="false" :with-auth="false" />
        <p v-if="type === 'http' || type === 'keyword'" class="-mt-3 text-[11px] text-muted-foreground">
          {{ t('templates.authHelp') }}
        </p>

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
          <div class="flex items-end pb-1">
            <Switch v-model="form.defaults!.active as boolean">{{ t('common.active') }}</Switch>
          </div>
          <div class="grid gap-1 sm:col-span-3">
            <Label for="template-default-description">{{ t('templates.monitorDescription') }}</Label>
            <Textarea id="template-default-description" v-model="form.defaults!.description" :rows="2" />
          </div>
        </section>

        <section v-if="supportsCertificate" class="grid gap-2 border-t border-border pt-4">
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

        <section v-if="supportsDomain" class="grid gap-2 border-t border-border pt-4">
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

        <section class="grid gap-2 border-t border-border pt-4">
          <Switch v-model="form.propagate as boolean">{{ t('templates.propagate') }}</Switch>
          <p class="text-[11px] text-muted-foreground">{{ t('templates.propagateHelp') }}</p>
        </section>
      </div>

      <template #footer>
        <Button variant="outline" @click="dialogOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="save">{{ t('common.save') }}</Button>
      </template>
    </Dialog>

    <Dialog
      v-model="linkOpen"
      :title="t('templates.linkAllTitle')"
      :description="linkWarning"
    >
      <div class="grid gap-3 text-xs">
        <p v-if="linkTarget" class="font-mono">{{ linkTarget.name }} · {{ linkTarget.type }}</p>

        <!--
          The scope is a decision, not a filter: it decides who follows the
          template and who stops following it. Three radio cards spell that out
          (a two-entry Select hid it), and the one of the last run is pre-selected
          because the template remembers it.
        -->
        <fieldset class="grid gap-2">
          <legend class="mb-1 text-[11px] font-medium">{{ t('templates.linkScope') }}</legend>
          <label
            v-for="card in linkScopeCards"
            :key="card.kind"
            class="flex cursor-pointer items-start gap-2 rounded-md border p-3 transition-colors"
            :class="linkScope === card.kind ? 'border-primary bg-primary/5' : 'border-border hover:border-primary/40'"
          >
            <input
              type="radio"
              name="link-scope"
              class="mt-0.5 h-3.5 w-3.5 accent-primary"
              :value="card.kind"
              :checked="linkScope === card.kind"
              @change="setLinkScope(card.kind)"
            />
            <span class="grid gap-0.5">
              <span class="text-xs font-medium">{{ card.title }}</span>
              <span class="text-[11px] text-muted-foreground">{{ card.description }}</span>
            </span>
          </label>
        </fieldset>

        <div v-if="linkScope === 'groups'" class="grid gap-2 rounded-md border border-border p-3">
          <p class="text-[11px] text-muted-foreground">
            {{ groups.length ? t('templates.linkScopeGroupsPick') : t('templates.linkNoGroups') }}
          </p>
          <div class="flex max-h-48 flex-wrap gap-4 overflow-y-auto">
            <Checkbox
              v-for="group in groups"
              :key="group.uuid"
              :model-value="linkGroupUUIDs.includes(group.uuid)"
              @update:model-value="toggleLinkGroup(group.uuid, $event)"
            >
              {{ group.name }} ({{ group.monitor_count }})
            </Checkbox>
          </div>
        </div>

        <div v-if="linkScope === 'tags'" class="grid gap-2 rounded-md border border-border p-3">
          <p class="text-[11px] text-muted-foreground">{{ t('templates.linkScopeTagsPick') }}</p>
          <div v-if="linkTagChoices.length" class="flex max-h-48 flex-wrap gap-4 overflow-y-auto">
            <Checkbox
              v-for="choice in linkTagChoices"
              :key="choice.tag"
              :model-value="linkTags.some((tag) => tag.toLowerCase() === choice.tag.toLowerCase())"
              @update:model-value="toggleLinkTag(choice.tag, $event)"
            >
              {{ choice.monitors === null ? choice.tag : `${choice.tag} (${choice.monitors})` }}
            </Checkbox>
          </div>
          <p v-else class="text-[11px] text-muted-foreground">{{ t('templates.linkNoTags') }}</p>
          <div class="flex items-center gap-2">
            <Input
              v-model="linkTagInput"
              class="max-w-[12rem]"
              :placeholder="t('templates.linkTagAddPlaceholder')"
              @keydown.enter.prevent="addLinkTag"
            />
            <Button variant="outline" size="sm" :disabled="!linkTagInput.trim()" @click="addLinkTag">
              {{ t('common.add') }}
            </Button>
          </div>
        </div>

        <p v-if="linkPreviewing" class="text-muted-foreground">{{ t('common.loading') }}</p>
        <p v-else-if="linkScopeNeedsSelection" class="text-muted-foreground">{{ t('templates.linkScopePick') }}</p>
        <p v-else-if="linkPreview">
          {{
            linkReleasesFollowers
              ? t('templates.linkAllPreviewGroups', {
                  monitors: linkPreview.monitors,
                  linked: linkPreview.linked,
                  updated: linkPreview.updated,
                  unlinked: linkPreview.unlinked,
                })
              : t('templates.linkAllPreview', {
                  monitors: linkPreview.monitors,
                  linked: linkPreview.linked,
                  updated: linkPreview.updated,
                })
          }}
        </p>
        <!--
          An empty selection is legitimate (the scope is a snapshot, so a tag no
          monitor carries yet is a valid answer), but releasing every follower on a
          typo is not: the run is called out before the confirmation.
        -->
        <p v-if="linkPreview && !linkPreview.monitors && linkPreview.unlinked" class="text-status-down">
          {{ t('templates.linkEmptyScope', { unlinked: linkPreview.unlinked }) }}
        </p>
      </div>
      <template #footer>
        <Button variant="outline" @click="linkOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="linking" :disabled="linkScopeNeedsSelection" @click="confirmLink">
          {{ t('templates.linkAllConfirm') }}
        </Button>
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
