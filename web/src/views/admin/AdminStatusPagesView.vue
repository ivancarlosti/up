<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { Boxes, ExternalLink, Pencil, Plus, Tag, Trash2, Users, X } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import ConfirmDialog from '@/components/ui/ConfirmDialog.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import Select from '@/components/ui/Select.vue'
import SortHeader from '@/components/ui/SortHeader.vue'
import Switch from '@/components/ui/Switch.vue'
import Textarea from '@/components/ui/Textarea.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { formatDateTime } from '@/lib/format'
import { sortStatusPages, statusPageSortKeys } from '@/lib/sort'
import type { StatusPageSortKey } from '@/lib/sort'
import { loadTableSort, toggleTableSort } from '@/lib/table-sort'
import { uptimeWindowOptionsWithInherit } from '@/lib/uptime-window'
import { copyToClipboard } from '@/lib/utils'
import { useToastStore } from '@/stores/toast'
import type { Monitor, MonitorGroup, Notification, StatusPage, StatusPageSubscriber } from '@/lib/types'

const { t, locale } = useI18n()
const toasts = useToastStore()

const pages = ref<StatusPage[]>([])
const monitors = ref<Monitor[]>([])
const groups = ref<MonitorGroup[]>([])
const notifications = ref<Notification[]>([])
const dialogOpen = ref(false)
const saving = ref(false)
const confirmOpen = ref(false)
const pendingRemoval = ref<StatusPage | null>(null)
const editing = ref<StatusPage | null>(null)
const selectedMonitors = ref<number[]>([])
const selectedGroups = ref<number[]>([])
const form = ref<Partial<StatusPage>>(blank())
// The term of the picker above the two selection lists (see the picker block
// below): it filters the monitors and the groups the operator can still add.
const pickerSearch = ref('')

// Filter and sort of the pages table: the text filter looks at the title, the
// slug and the description, and the sort choice is remembered per browser
// (lib/table-sort.ts).
const pageSearch = ref('')
const PAGE_SORT_STORAGE_KEY = 'up.admin.status-pages.sort'
const sort = ref(
  loadTableSort({
    storageKey: PAGE_SORT_STORAGE_KEY,
    keys: statusPageSortKeys,
    defaultKey: 'title',
  }),
)

/** toggleSort switches the column, or flips the direction of the current one. */
function toggleSort(key: StatusPageSortKey): void {
  sort.value = toggleTableSort(sort.value, key, PAGE_SORT_STORAGE_KEY)
}

/** filteredPages applies the text filter of the table. */
const filteredPages = computed(() => {
  const term = pageSearch.value.trim().toLowerCase()
  if (!term) return pages.value
  return pages.value.filter((page) =>
    [page.title, page.slug, page.description].some((value) => (value ?? '').toLowerCase().includes(term)),
  )
})

/** sortedPages applies the chosen column to the filtered rows (see lib/sort.ts). */
const sortedPages = computed(() =>
  sortStatusPages(filteredPages.value, sort.value.key, sort.value.direction, { locale: locale.value }),
)

const themeOptions = computed(() => [
  { value: 'system', label: t('settings.themeSystem') },
  { value: 'light', label: t('settings.themeLight') },
  { value: 'dark', label: t('settings.themeDark') },
])

/** The uptime periods a page may show; 0 keeps the global one. */
const windowOptions = computed(() => uptimeWindowOptionsWithInherit(t('statusPages.uptimeWindowInherit')))

/** Only SMTP channels can carry the subscription mails. */
const smtpChannels = computed(() =>
  notifications.value.filter((channel) => channel.type === 'smtp' && channel.active),
)
const smtpChannelOptions = computed(() => [
  { value: '', label: t('statusPages.subscriptionChannelNone') },
  ...smtpChannels.value.map((channel) => ({
    value: channel.uuid,
    label: channel.active ? channel.name : `${channel.name} (${t('common.disabled')})`,
  })),
])

// --- subscriber listing ---------------------------------------------------
const subscriberDialogOpen = ref(false)
const subscriberPage = ref<StatusPage | null>(null)
const subscribers = ref<StatusPageSubscriber[]>([])
const subscriberTotal = ref(0)
const subscriberNumber = ref(1)
const subscriberSearch = ref('')
const subscriberLoading = ref(false)
const subscriberSize = 25

const subscriberTotalPages = computed(() =>
  Math.max(1, Math.ceil(subscriberTotal.value / subscriberSize)),
)

/** subscriptionDelay adapts the numeric delay to the string Input. */
const subscriptionDelay = computed({
  get: () => String(form.value.subscription_delay_minutes ?? 10),
  set: (value: string) => {
    const parsed = Number(value)
    form.value.subscription_delay_minutes = Number.isFinite(parsed) ? parsed : 10
  },
})

/** uptimeWindow adapts the numeric window to the string Select. */
const uptimeWindow = computed({
  get: () => String(form.value.uptime_window_hours ?? 0),
  set: (value: string) => {
    form.value.uptime_window_hours = Number(value)
  },
})

function blank(): Partial<StatusPage> {
  return {
    slug: '',
    title: '',
    description: '',
    footer_text: '',
    theme: 'system',
    is_public: true,
    show_uptime: true,
    show_charts: true,
    show_tags: false,
    show_cert_expiry: false,
    show_domain_expiry: false,
    uptime_window_hours: 0,
    custom_css: '',
    subscription_enabled: false,
    subscription_smtp_uuid: '',
    subscription_delay_minutes: 10,
  }
}

function publicURL(slug: string): string {
  const base = window.location.origin
  return `${base}/status/${slug}`
}

function badgeURL(slug: string): string {
  return `${publicURL(slug)}/badge.svg`.replace('/status/', '/api/public/status/')
}

async function load(): Promise<void> {
  try {
    const [pageList, monitorList, groupList, channelList] = await Promise.all([
      api.statusPages(),
      api.monitors({ decorate: 'false' }),
      api.monitorGroups(),
      api.notifications(),
    ])
    pages.value = pageList
    monitors.value = monitorList
    groups.value = groupList
    notifications.value = channelList ?? []
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function openEdit(page: StatusPage): Promise<void> {
  editing.value = page
  form.value = JSON.parse(JSON.stringify(page))
  pickerSearch.value = ''
  const items = await api.statusPageMonitors(page.id)
  selectedMonitors.value = items.map((item) => item.monitor_id)
  const links = await api.statusPageGroups(page.id)
  selectedGroups.value = links.map((link) => link.group_id)
  dialogOpen.value = true
}

function openCreate(): void {
  editing.value = null
  form.value = blank()
  selectedMonitors.value = []
  selectedGroups.value = []
  pickerSearch.value = ''
  dialogOpen.value = true
}

async function openSubscribers(page: StatusPage): Promise<void> {
  subscriberPage.value = page
  subscriberSearch.value = ''
  subscriberNumber.value = 1
  subscriberDialogOpen.value = true
  await loadSubscribers()
}

async function loadSubscribers(): Promise<void> {
  if (!subscriberPage.value) return
  subscriberLoading.value = true
  try {
    const result = await api.statusPageSubscribers(subscriberPage.value.id, {
      page: subscriberNumber.value,
      size: subscriberSize,
      q: subscriberSearch.value.trim() || undefined,
    })
    subscribers.value = result.subscribers ?? []
    subscriberTotal.value = result.total ?? 0
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    subscriberLoading.value = false
  }
}

async function changeSubscriberPage(delta: number): Promise<void> {
  const next = subscriberNumber.value + delta
  if (next < 1 || next > subscriberTotalPages.value) return
  subscriberNumber.value = next
  await loadSubscribers()
}

async function searchSubscribers(): Promise<void> {
  subscriberNumber.value = 1
  await loadSubscribers()
}

async function removeSubscriber(subscriber: StatusPageSubscriber): Promise<void> {
  if (!subscriberPage.value) return
  try {
    await api.deleteStatusPageSubscriber(subscriberPage.value.id, subscriber.id)
    toasts.success(t('common.deleted'))
    await loadSubscribers()
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

/** subscriberState is the label of the three states a subscriber can be in. */
function subscriberState(subscriber: StatusPageSubscriber): string {
  if (subscriber.unsubscribed_at) return t('statusPages.subscriberUnsubscribed')
  if (subscriber.confirmed_at) return t('statusPages.subscriberConfirmed')
  return t('statusPages.subscriberPending')
}

function subscriberStateClass(subscriber: StatusPageSubscriber): 'secondary' | 'success' | 'warning' {
  if (subscriber.unsubscribed_at) return 'secondary'
  if (subscriber.confirmed_at) return 'success'
  return 'warning'
}


// --- the picker of the dialog ----------------------------------------------
// The same search-and-pick pair as the monitor group dialog (AdminMonitorGroupsView),
// extended to the groups: typing filters both kinds and a click moves the row into
// the list it belongs to. The two selections stay separate lists because the page
// renders the explicit monitors first, in their order, and then one section per
// group: one merged list would promise an order the backend does not keep.

/** pickerTerm is the trimmed search term of the picker, lower cased once. */
const pickerTerm = computed(() => pickerSearch.value.trim().toLowerCase())

/** matchingMonitors applies the term to the name, the type and the tags. */
const matchingMonitors = computed(() => {
  const term = pickerTerm.value
  if (!term) return monitors.value
  return monitors.value.filter((monitor) =>
    [monitor.name, monitor.type, monitor.tags].some((value) => (value ?? '').toLowerCase().includes(term)),
  )
})

/** matchingGroups applies it to the name and the description. */
const matchingGroups = computed(() => {
  const term = pickerTerm.value
  if (!term) return groups.value
  return groups.value.filter((group) =>
    [group.name, group.description].some((value) => (value ?? '').toLowerCase().includes(term)),
  )
})

/**
 * coveredByGroup maps every monitor a selected group already publishes to the
 * name of that group: the page shows it without an explicit entry, so the picker
 * reports it instead of offering the same monitor a second time.
 */
const coveredByGroup = computed(() => {
  const covered = new Map<number, string>()
  for (const group of groups.value) {
    if (!selectedGroups.value.includes(group.id)) continue
    for (const monitorId of group.monitor_ids ?? []) covered.set(monitorId, group.name)
  }
  return covered
})

/** offeredMonitors are the monitors the operator can still add (covered ones flagged). */
const offeredMonitors = computed(() =>
  matchingMonitors.value
    .filter((monitor) => !selectedMonitors.value.includes(monitor.id))
    .map((monitor) => ({ monitor, coveredBy: coveredByGroup.value.get(monitor.id) ?? '' })),
)

/** offeredGroups are the groups not linked to the page yet. */
const offeredGroups = computed(() =>
  matchingGroups.value.filter((group) => !selectedGroups.value.includes(group.id)),
)

/** pickerEmpty hides the offer box once there is nothing left to add. */
const pickerEmpty = computed(() => offeredMonitors.value.length === 0 && offeredGroups.value.length === 0)

/** pickerHasMatch tells "no result for the term" from "everything is already here". */
const pickerHasMatch = computed(
  () => matchingMonitors.value.length > 0 || matchingGroups.value.length > 0,
)

/** selectedMonitorRows resolves the explicit selection into the rows below the picker. */
const selectedMonitorRows = computed(() => {
  const byId = new Map(monitors.value.map((monitor) => [monitor.id, monitor]))
  return selectedMonitors.value
    .map((id) => byId.get(id))
    .filter((monitor): monitor is Monitor => monitor !== undefined)
})

/** selectedGroupRows resolves the linked groups into the rows below the picker. */
const selectedGroupRows = computed(() => {
  const byId = new Map(groups.value.map((group) => [group.id, group]))
  return selectedGroups.value
    .map((id) => byId.get(id))
    .filter((group): group is MonitorGroup => group !== undefined)
})

/** tagList splits the comma separated tags of a monitor into badge labels. */
function tagList(monitor: Monitor): string[] {
  return (monitor.tags ?? '')
    .split(',')
    .map((tag) => tag.trim())
    .filter(Boolean)
}

/** addMonitor appends a monitor at the end of the displayed monitors (their order). */
function addMonitor(id: number): void {
  if (selectedMonitors.value.includes(id)) return
  selectedMonitors.value = [...selectedMonitors.value, id]
}

/** removeMonitor takes it off the page. */
function removeMonitor(id: number): void {
  selectedMonitors.value = selectedMonitors.value.filter((current) => current !== id)
}

/** addGroup appends a group: its section renders after the explicit monitors. */
function addGroup(id: number): void {
  if (selectedGroups.value.includes(id)) return
  selectedGroups.value = [...selectedGroups.value, id]
}

/** removeGroup unlinks it, so the monitors it published leave the page with it. */
function removeGroup(id: number): void {
  selectedGroups.value = selectedGroups.value.filter((current) => current !== id)
}

async function save(): Promise<void> {
  saving.value = true
  try {
    if (editing.value) {
      await api.updateStatusPage(editing.value.id, form.value)
      await api.setStatusPageMonitors(editing.value.id, selectedMonitors.value)
      await api.setStatusPageGroups(editing.value.id, selectedGroups.value)
    } else {
      const created = await api.createStatusPage({ ...form.value, monitor_ids: selectedMonitors.value })
      if (created?.id) await api.setStatusPageGroups(created.id, selectedGroups.value)
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
    await api.deleteStatusPage(pendingRemoval.value.id)
    pages.value = pages.value.filter((page) => page.id !== pendingRemoval.value?.id)
    confirmOpen.value = false
    toasts.success(t('common.deleted'))
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function copy(value: string): Promise<void> {
  if (await copyToClipboard(value)) toasts.success(t('common.copied'))
}

onMounted(load)
</script>


<template>
  <div class="flex flex-col gap-4">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold">{{ t('statusPages.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('statusPages.subtitle') }}</p>
      </div>
      <Button size="sm" @click="openCreate">
        <Plus class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('statusPages.new') }}
      </Button>
    </header>

    <EmptyState v-if="!pages.length" :title="t('statusPages.empty')" :description="t('statusPages.emptyHint')" />

    <Card v-else :padded="false">
      <div class="flex flex-wrap items-center gap-2 border-b border-border px-5 py-4">
        <Input v-model="pageSearch" class="max-w-xs" :placeholder="t('statusPages.filterPlaceholder')" />
        <span class="text-[11px] text-muted-foreground">
          {{ t('common.shownOfTotal', { shown: filteredPages.length, total: pages.length }) }}
        </span>
      </div>

      <p v-if="!filteredPages.length" class="p-5 text-xs text-muted-foreground">
        {{ t('common.noMatch') }}
      </p>

      <div v-else class="overflow-x-auto">
        <table class="data-table">
          <thead>
            <tr>
              <SortHeader
                :label="t('common.name')"
                column="title"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('title')"
              />
              <SortHeader
                :label="t('statusPages.slug')"
                column="slug"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('slug')"
              />
              <SortHeader
                :label="t('statusPages.visibility')"
                column="visibility"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('visibility')"
              />
              <SortHeader
                :label="t('statusPages.monitors')"
                column="monitors"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('monitors')"
              />
              <SortHeader
                :label="t('statusPages.groups')"
                column="groups"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('groups')"
              />
              <th class="text-end">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="page in sortedPages" :key="page.id">
              <td>
                <span class="block text-sm font-medium">{{ page.title }}</span>
                <span v-if="page.description" class="block text-[11px] text-muted-foreground">
                  {{ page.description }}
                </span>
              </td>
              <td class="whitespace-nowrap">
                <code class="rounded bg-muted px-1.5 py-0.5 text-[11px]">/status/{{ page.slug }}</code>
              </td>
              <td>
                <Badge :variant="page.is_public ? 'success' : 'secondary'">
                  {{ page.is_public ? t('statusPages.isPublic') : t('common.disabled') }}
                </Badge>
              </td>
              <td class="whitespace-nowrap tabular-nums">{{ page.monitors_count }}</td>
              <td class="whitespace-nowrap tabular-nums">{{ page.groups_count ?? 0 }}</td>
              <td class="text-end">
                <div class="flex items-center justify-end gap-1">
                  <RouterLink :to="{ name: 'status-page', params: { slug: page.slug } }" target="_blank">
                    <Button variant="ghost" size="sm" :title="t('statusPages.publicUrl')" :aria-label="t('statusPages.publicUrl')">
                      <ExternalLink class="h-3.5 w-3.5" aria-hidden="true" />
                    </Button>
                  </RouterLink>
                  <Button
                    variant="ghost"
                    size="sm"
                    :title="t('statusPages.badge')"
                    :aria-label="t('statusPages.badge')"
                    @click="copy(badgeURL(page.slug))"
                  >
                    <Tag class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    :title="t('statusPages.subscribers')"
                    :aria-label="t('statusPages.subscribers')"
                    @click="openSubscribers(page)"
                  >
                    <Users class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    :title="t('common.edit')"
                    :aria-label="t('common.edit')"
                    @click="openEdit(page)"
                  >
                    <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    class="text-status-down"
                    :title="t('common.delete')"
                    :aria-label="t('common.delete')"
                    @click="(pendingRemoval = page), (confirmOpen = true)"
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

    <Dialog v-model="dialogOpen" :title="editing ? t('statusPages.edit') : t('statusPages.new')" wide>
      <div class="grid gap-3 sm:grid-cols-2">
        <div class="grid gap-1">
          <Label for="page-title" required>{{ t('app.name') }}</Label>
          <Input id="page-title" v-model="form.title" placeholder="Service status" />
        </div>
        <div class="grid gap-1">
          <Label for="page-slug" required :help="t('statusPages.slugHelp')">{{ t('statusPages.slug') }}</Label>
          <Input id="page-slug" v-model="form.slug" placeholder="main" />
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="page-description">{{ t('common.description') }}</Label>
          <Input id="page-description" v-model="form.description" />
        </div>
        <div class="grid gap-1">
          <Label for="page-theme">{{ t('statusPages.theme') }}</Label>
          <Select id="page-theme" v-model="form.theme" :options="themeOptions" />
        </div>
        <div class="grid gap-1">
          <Label for="page-footer">{{ t('statusPages.footer') }}</Label>
          <Input id="page-footer" v-model="form.footer_text" />
        </div>
        <div class="grid gap-1">
          <Label for="page-uptime-window" :help="t('statusPages.uptimeWindowHelp')">
            {{ t('statusPages.uptimeWindow') }}
          </Label>
          <Select id="page-uptime-window" v-model="uptimeWindow" :options="windowOptions" />
        </div>
        <div class="flex flex-wrap items-center gap-4 sm:col-span-2">
          <Switch v-model="form.is_public as boolean">{{ t('statusPages.isPublic') }}</Switch>
          <Switch v-model="form.show_uptime as boolean">{{ t('statusPages.showUptime') }}</Switch>
          <Switch v-model="form.show_charts as boolean">{{ t('statusPages.showCharts') }}</Switch>
          <Switch v-model="form.show_tags as boolean">{{ t('statusPages.showTags') }}</Switch>
          <Switch v-model="form.show_cert_expiry as boolean">{{ t('statusPages.showCertExpiry') }}</Switch>
          <Switch v-model="form.show_domain_expiry as boolean">{{ t('statusPages.showDomainExpiry') }}</Switch>
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="page-css">{{ t('statusPages.customCss') }}</Label>
          <Textarea id="page-css" v-model="form.custom_css" :rows="3" />
        </div>
        <div class="grid gap-3 rounded-lg border border-border p-3 sm:col-span-2">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <div>
              <p class="text-sm font-medium">{{ t('statusPages.subscriptionTitle') }}</p>
              <p class="text-[11px] text-muted-foreground">{{ t('statusPages.subscriptionHelp') }}</p>
            </div>
            <Switch v-model="form.subscription_enabled as boolean">{{ t('statusPages.subscriptionEnabled') }}</Switch>
          </div>
          <div v-if="form.subscription_enabled" class="grid gap-3 sm:grid-cols-2">
            <div class="grid gap-1">
              <Label for="page-subscription-channel" :help="t('statusPages.subscriptionChannelHelp')">
                {{ t('statusPages.subscriptionChannel') }}
              </Label>
              <Select
                id="page-subscription-channel"
                v-model="form.subscription_smtp_uuid"
                :options="smtpChannelOptions"
              />
              <p v-if="!smtpChannels.length" class="text-[11px] text-status-degraded">
                {{ t('statusPages.subscriptionNoChannels') }}
              </p>
            </div>
            <div class="grid gap-1">
              <Label for="page-subscription-delay" :help="t('statusPages.subscriptionDelayHelp')">
                {{ t('statusPages.subscriptionDelay') }}
              </Label>
              <Input
                id="page-subscription-delay"
                v-model="subscriptionDelay"
                type="number"
                min="5"
                max="1440"
                step="5"
              />
            </div>
          </div>
        </div>
        <div class="grid gap-2 sm:col-span-2">
          <Label :help="t('statusPages.pickerHelp')">{{ t('statusPages.pickerTitle') }}</Label>
          <Input
            id="status-page-picker"
            v-model="pickerSearch"
            :placeholder="t('statusPages.pickerPlaceholder')"
          />
          <!--
            The picker: every monitor and group matching the search that is not on
            the page yet. A click appends it to the list it belongs to below. A
            monitor a selected group already publishes is listed but not clickable,
            so the same monitor is never added twice.
          -->
          <div v-if="!pickerEmpty" class="max-h-56 overflow-y-auto rounded-md border border-border">
            <template v-if="offeredMonitors.length">
              <p class="border-b border-border bg-muted px-3 py-1 text-[11px] font-medium text-muted-foreground">
                {{ t('statusPages.monitors') }}
              </p>
              <button
                v-for="offer in offeredMonitors"
                :key="`monitor-${offer.monitor.id}`"
                type="button"
                class="flex w-full flex-wrap items-center gap-2 border-b border-border px-3 py-1.5 text-start text-sm last:border-b-0"
                :class="offer.coveredBy ? 'cursor-default text-muted-foreground' : 'hover:bg-muted'"
                :disabled="Boolean(offer.coveredBy)"
                :title="offer.coveredBy
                  ? t('statusPages.pickerCovered', { group: offer.coveredBy })
                  : t('statusPages.pickerAddMonitor')"
                :aria-label="offer.coveredBy
                  ? t('statusPages.pickerCovered', { group: offer.coveredBy })
                  : t('statusPages.pickerAddMonitor')"
                @click="addMonitor(offer.monitor.id)"
              >
                <Boxes v-if="offer.coveredBy" class="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                <Plus v-else class="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span class="min-w-0 truncate">{{ offer.monitor.name }}</span>
                <Badge variant="secondary" class="ms-auto shrink-0">{{ offer.monitor.type }}</Badge>
                <Badge v-if="offer.coveredBy" variant="outline" class="min-w-0">
                  <span class="min-w-0 truncate">
                    {{ t('statusPages.pickerVia', { group: offer.coveredBy }) }}
                  </span>
                </Badge>
              </button>
            </template>
            <template v-if="offeredGroups.length">
              <p class="border-b border-border bg-muted px-3 py-1 text-[11px] font-medium text-muted-foreground">
                {{ t('statusPages.groups') }}
              </p>
              <button
                v-for="group in offeredGroups"
                :key="`group-${group.id}`"
                type="button"
                class="flex w-full flex-wrap items-center gap-2 border-b border-border px-3 py-1.5 text-start text-sm last:border-b-0 hover:bg-muted"
                :title="t('statusPages.pickerAddGroup')"
                :aria-label="t('statusPages.pickerAddGroup')"
                @click="addGroup(group.id)"
              >
                <Plus class="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                <span class="min-w-0 truncate">{{ group.name }}</span>
                <Badge variant="outline" class="ms-auto shrink-0">
                  {{ t('statusPages.pickerGroupSize', { count: group.monitor_count }) }}
                </Badge>
              </button>
            </template>
          </div>
          <p v-else class="text-[11px] text-muted-foreground">
            {{ pickerHasMatch ? t('statusPages.pickerAllAdded') : t('common.noMatch') }}
          </p>
          <p v-if="!groups.length" class="text-[11px] text-muted-foreground">
            {{ t('statusPages.pickerNoGroupsHint') }}
          </p>

          <!-- The monitors of the page, in display order: they render before the groups. -->
          <Label class="mt-1">{{ t('statusPages.selectMonitors') }} ({{ selectedMonitors.length }})</Label>
          <div v-if="selectedMonitorRows.length" class="flex flex-col gap-1">
            <div
              v-for="monitor in selectedMonitorRows"
              :key="monitor.id"
              class="flex flex-wrap items-center gap-2 rounded-md border border-border px-3 py-1.5 text-sm"
            >
              <span class="min-w-0 truncate font-medium">{{ monitor.name }}</span>
              <Badge variant="secondary" class="shrink-0">{{ monitor.type }}</Badge>
              <Badge v-for="tag in tagList(monitor)" :key="tag" variant="outline" class="shrink-0">
                {{ tag }}
              </Badge>
              <Badge v-if="coveredByGroup.get(monitor.id)" variant="outline" class="min-w-0">
                <span class="min-w-0 truncate">
                  {{ t('statusPages.pickerAlsoIn', { group: coveredByGroup.get(monitor.id) }) }}
                </span>
              </Badge>
              <Button
                variant="ghost"
                size="sm"
                class="ms-auto shrink-0 text-status-down"
                :title="t('statusPages.pickerRemoveMonitor')"
                :aria-label="t('statusPages.pickerRemoveMonitor')"
                @click="removeMonitor(monitor.id)"
              >
                <X class="h-3.5 w-3.5" aria-hidden="true" />
              </Button>
            </div>
          </div>
          <p v-else class="text-[11px] text-muted-foreground">{{ t('statusPages.pickerNoMonitors') }}</p>

          <!-- The linked groups: each one renders its own section after the monitors. -->
          <Label class="mt-1">{{ t('statusPages.selectGroups') }} ({{ selectedGroups.length }})</Label>
          <div v-if="selectedGroupRows.length" class="flex flex-col gap-1">
            <div
              v-for="group in selectedGroupRows"
              :key="group.id"
              class="flex flex-wrap items-center gap-2 rounded-md border border-border px-3 py-1.5 text-sm"
            >
              <Boxes class="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
              <span class="min-w-0 truncate font-medium">{{ group.name }}</span>
              <Badge variant="outline" class="ms-auto shrink-0">
                {{ t('statusPages.pickerGroupSize', { count: group.monitor_count }) }}
              </Badge>
              <Button
                variant="ghost"
                size="sm"
                class="shrink-0 text-status-down"
                :title="t('statusPages.pickerRemoveGroup')"
                :aria-label="t('statusPages.pickerRemoveGroup')"
                @click="removeGroup(group.id)"
              >
                <X class="h-3.5 w-3.5" aria-hidden="true" />
              </Button>
            </div>
          </div>
          <p v-else class="text-[11px] text-muted-foreground">{{ t('statusPages.pickerNoGroups') }}</p>
        </div>
      </div>

      <template #footer>
        <Button variant="outline" @click="dialogOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="save">{{ t('common.save') }}</Button>
      </template>
    </Dialog>

    <Dialog
      v-model="subscriberDialogOpen"
      :title="t('statusPages.subscribersTitle', { page: subscriberPage?.title ?? '' })"
      :description="t('statusPages.subscribersHelp')"
      wide
    >
      <div class="flex flex-wrap items-center gap-2">
        <Input
          v-model="subscriberSearch"
          class="max-w-xs"
          :placeholder="t('statusPages.subscriberSearch')"
          @keyup.enter="searchSubscribers"
        />
        <Button variant="outline" size="sm" :loading="subscriberLoading" @click="searchSubscribers">
          {{ t('common.search') }}
        </Button>
        <span class="text-[11px] text-muted-foreground">
          {{ t('statusPages.subscriberCount', { total: subscriberTotal }) }}
        </span>
      </div>

      <p v-if="!subscriberLoading && !subscribers.length" class="mt-4 text-xs text-muted-foreground">
        {{ t('statusPages.subscriberEmpty') }}
      </p>

      <div v-else class="mt-4 overflow-x-auto">
        <table class="data-table">
          <thead>
            <tr>
              <th>{{ t('login.email') }}</th>
              <th>{{ t('statusPages.subscriberState') }}</th>
              <th>{{ t('statusPages.subscriberLocale') }}</th>
              <th>{{ t('statusPages.subscriberCreated') }}</th>
              <th class="text-end">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="subscriber in subscribers" :key="subscriber.id">
              <td class="text-sm">{{ subscriber.email }}</td>
              <td>
                <Badge :variant="subscriberStateClass(subscriber)">{{ subscriberState(subscriber) }}</Badge>
              </td>
              <td class="text-xs text-muted-foreground">{{ subscriber.locale }}</td>
              <td class="whitespace-nowrap text-xs text-muted-foreground">
                {{ formatDateTime(subscriber.created_at, locale) }}
              </td>
              <td class="text-end">
                <Button
                  variant="ghost"
                  size="sm"
                  class="text-status-down"
                  :title="t('common.delete')"
                  :aria-label="t('common.delete')"
                  @click="removeSubscriber(subscriber)"
                >
                  <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>

      <template #footer>
        <span class="me-auto text-[11px] text-muted-foreground">
          {{ t('statusPages.subscriberPage', { page: subscriberNumber, pages: subscriberTotalPages }) }}
        </span>
        <Button
          variant="outline"
          size="sm"
          :disabled="subscriberNumber <= 1 || subscriberLoading"
          @click="changeSubscriberPage(-1)"
        >
          {{ t('common.previous') }}
        </Button>
        <Button
          variant="outline"
          size="sm"
          :disabled="subscriberNumber >= subscriberTotalPages || subscriberLoading"
          @click="changeSubscriberPage(1)"
        >
          {{ t('common.next') }}
        </Button>
      </template>
    </Dialog>

    <ConfirmDialog
      v-model="confirmOpen"
      :title="t('statusPages.deleteTitle')"
      :description="t('statusPages.deleteWarning')"
      :confirm-label="t('common.delete')"
      @confirm="confirmRemove"
    />
  </div>
</template>
