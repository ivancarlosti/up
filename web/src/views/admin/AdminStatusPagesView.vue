<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { ExternalLink, Pencil, Plus, Trash2 } from 'lucide-vue-next'
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
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { copyToClipboard } from '@/lib/utils'
import { useToastStore } from '@/stores/toast'
import type { Monitor, MonitorGroup, StatusPage } from '@/lib/types'

const { t } = useI18n()
const toasts = useToastStore()

const pages = ref<StatusPage[]>([])
const monitors = ref<Monitor[]>([])
const groups = ref<MonitorGroup[]>([])
const dialogOpen = ref(false)
const saving = ref(false)
const confirmOpen = ref(false)
const pendingRemoval = ref<StatusPage | null>(null)
const editing = ref<StatusPage | null>(null)
const selectedMonitors = ref<number[]>([])
const selectedGroups = ref<number[]>([])
const form = ref<Partial<StatusPage>>(blank())

const themeOptions = computed(() => [
  { value: 'system', label: t('settings.themeSystem') },
  { value: 'light', label: t('settings.themeLight') },
  { value: 'dark', label: t('settings.themeDark') },
])

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
    custom_css: '',
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
    const [pageList, monitorList, groupList] = await Promise.all([
      api.statusPages(),
      api.monitors({ decorate: 'false' }),
      api.monitorGroups(),
    ])
    pages.value = pageList
    monitors.value = monitorList
    groups.value = groupList
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  }
}

async function openEdit(page: StatusPage): Promise<void> {
  editing.value = page
  form.value = JSON.parse(JSON.stringify(page))
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
  dialogOpen.value = true
}

function toggleMonitor(id: number, value: boolean): void {
  const set = new Set(selectedMonitors.value)
  if (value) set.add(id)
  else set.delete(id)
  selectedMonitors.value = [...set]
}

function toggleGroup(id: number, value: boolean): void {
  const set = new Set(selectedGroups.value)
  if (value) set.add(id)
  else set.delete(id)
  selectedGroups.value = [...set]
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

    <div v-else class="grid gap-3 lg:grid-cols-2">
      <Card v-for="page in pages" :key="page.id" :title="page.title" :description="page.description">
        <div class="flex flex-wrap items-center gap-2 text-[11px]">
          <Badge :variant="page.is_public ? 'success' : 'secondary'">
            {{ page.is_public ? t('statusPages.isPublic') : t('common.disabled') }}
          </Badge>
          <Badge variant="outline">{{ t('statusPages.selectMonitors') }}: {{ page.monitors_count }}</Badge>
          <Badge v-if="page.groups_count" variant="outline">
            {{ t('statusPages.selectGroups') }}: {{ page.groups_count }}
          </Badge>
          <code class="rounded bg-muted px-1.5 py-0.5">/status/{{ page.slug }}</code>
        </div>

        <div class="mt-3 flex flex-wrap items-center gap-2">
          <Button variant="outline" size="sm" @click="copy(publicURL(page.slug))">
            <ExternalLink class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('statusPages.publicUrl') }}
          </Button>
          <Button variant="outline" size="sm" @click="copy(badgeURL(page.slug))">{{ t('statusPages.badge') }}</Button>
        </div>

        <template #footer>
          <RouterLink :to="{ name: 'status-page', params: { slug: page.slug } }" target="_blank">
            <Button variant="ghost" size="sm">{{ t('statusPages.publicUrl') }}</Button>
          </RouterLink>
          <Button variant="ghost" size="sm" @click="openEdit(page)">
            <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('common.edit') }}
          </Button>
          <Button
            variant="ghost"
            size="sm"
            class="text-status-down"
            @click="(pendingRemoval = page), (confirmOpen = true)"
          >
            <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
            {{ t('common.delete') }}
          </Button>
        </template>
      </Card>
    </div>

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
        <div class="flex flex-wrap items-center gap-4 sm:col-span-2">
          <Switch v-model="form.is_public as boolean">{{ t('statusPages.isPublic') }}</Switch>
          <Switch v-model="form.show_uptime as boolean">{{ t('statusPages.showUptime') }}</Switch>
          <Switch v-model="form.show_charts as boolean">{{ t('statusPages.showCharts') }}</Switch>
          <Switch v-model="form.show_tags as boolean">{{ t('statusPages.showTags') }}</Switch>
        </div>
        <div class="grid gap-1 sm:col-span-2">
          <Label for="page-css">{{ t('statusPages.customCss') }}</Label>
          <Textarea id="page-css" v-model="form.custom_css" :rows="3" />
        </div>
        <div class="grid gap-2 sm:col-span-2">
          <Label :help="t('statusPages.selectMonitorsHelp')">{{ t('statusPages.selectMonitors') }}</Label>
          <div class="flex flex-wrap gap-4">
            <Checkbox
              v-for="monitor in monitors"
              :key="monitor.id"
              :model-value="selectedMonitors.includes(monitor.id)"
              @update:model-value="toggleMonitor(monitor.id, $event)"
            >
              {{ monitor.name }}
            </Checkbox>
          </div>
        </div>
        <div v-if="groups.length" class="grid gap-2 sm:col-span-2">
          <Label :help="t('statusPages.selectGroupsHelp')">{{ t('statusPages.selectGroups') }}</Label>
          <div class="flex flex-wrap gap-4">
            <Checkbox
              v-for="group in groups"
              :key="group.id"
              :model-value="selectedGroups.includes(group.id)"
              @update:model-value="toggleGroup(group.id, $event)"
            >
              {{ group.name }} ({{ group.monitor_count }})
            </Checkbox>
          </div>
        </div>
      </div>

      <template #footer>
        <Button variant="outline" @click="dialogOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="saving" @click="save">{{ t('common.save') }}</Button>
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
