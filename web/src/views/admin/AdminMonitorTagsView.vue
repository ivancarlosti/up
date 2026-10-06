<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Pencil, RefreshCw, Tags, TriangleAlert } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import Dialog from '@/components/ui/Dialog.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import Input from '@/components/ui/Input.vue'
import Label from '@/components/ui/Label.vue'
import SortHeader from '@/components/ui/SortHeader.vue'
import { api } from '@/lib/api'
import { translateError } from '@/lib/errors'
import { monitorTagSortKeys, sortMonitorTags } from '@/lib/sort'
import type { MonitorTagSortKey } from '@/lib/sort'
import { loadTableSort, toggleTableSort } from '@/lib/table-sort'
import { useToastStore } from '@/stores/toast'
import type { BulkTagReport, TagUsage } from '@/lib/types'

/**
 * The monitor tags page.
 *
 * Tags have no ids (Monitor.Tags is a free form list), so there is nothing to
 * create, order or delete here: the page lists the tags that are actually in use
 * with how many monitors carry each of them, and renames one. A rename is the
 * only write, and it is a bulk edit over every monitor carrying the tag (the
 * server resolves the selection from the tag, previews it with a dry run and
 * then applies it).
 */
const { t, locale } = useI18n()
const toasts = useToastStore()

const tags = ref<TagUsage[]>([])
const loading = ref(false)

// Filter and sort of the tags table: the text filter narrows the rows, the sort
// choice is remembered per browser (lib/table-sort.ts, the same helper the
// monitors, the groups and the expiry tables use). The default is the tag name.
const tagSearch = ref('')
const TAG_SORT_STORAGE_KEY = 'up.admin.monitor-tags.sort'
const sort = ref(
  loadTableSort({
    storageKey: TAG_SORT_STORAGE_KEY,
    keys: monitorTagSortKeys,
    defaultKey: 'tag',
  }),
)

/** toggleSort switches the column, or flips the direction of the current one. */
function toggleSort(key: MonitorTagSortKey): void {
  sort.value = toggleTableSort(sort.value, key, TAG_SORT_STORAGE_KEY)
}

/** The rename dialog: the tag being renamed, its new name and the dry run. */
const renameOpen = ref(false)
const renaming = ref<TagUsage | null>(null)
const newName = ref('')
const preview = ref<BulkTagReport | null>(null)
const applying = ref(false)

/** filteredTags applies the text filter of the table (the tag name). */
const filteredTags = computed(() => {
  const term = tagSearch.value.trim().toLowerCase()
  if (!term) return tags.value
  return tags.value.filter((row) => row.tag.toLowerCase().includes(term))
})

/** sortedTags applies the chosen column to the filtered rows (see lib/sort.ts). */
const sortedTags = computed(() =>
  sortMonitorTags(filteredTags.value, sort.value.key, sort.value.direction, { locale: locale.value }),
)

/**
 * mergeTarget is the tag the rename would merge into: an existing tag (other
 * than the one being renamed) that already spells the new name. Tags have no id
 * and are compared whole and case insensitively, so renaming onto an existing
 * name merges the monitors, which the dialog warns about before applying.
 */
const mergeTarget = computed(() => {
  const target = newName.value.trim().toLowerCase()
  if (!target || !renaming.value) return null
  if (target === renaming.value.tag.toLowerCase()) return null
  return tags.value.find((row) => row.tag.toLowerCase() === target) ?? null
})

async function load(): Promise<void> {
  loading.value = true
  try {
    tags.value = await api.monitorTags()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    loading.value = false
  }
}

/** openRename prepares the dialog on a tag, with its own name as the starting point. */
function openRename(row: TagUsage): void {
  renaming.value = row
  newName.value = row.tag
  preview.value = null
  renameOpen.value = true
}

// Any edit of the name invalidates the preview: the numbers the operator is
// about to confirm must describe the name currently typed, not an earlier one.
watch(newName, () => {
  preview.value = null
})

/**
 * submit previews the rename on the first click (a dry run: how many monitors
 * change and how many are already up to date) and applies it on the second, so
 * the operator always confirms numbers that were read from the server. The
 * rename is "everything tagged old, minus old, plus new".
 */
async function submit(): Promise<void> {
  if (!renaming.value) return
  const from = renaming.value.tag
  const to = newName.value.trim()
  if (!to || to === from) {
    renameOpen.value = false
    return
  }
  applying.value = true
  try {
    if (!preview.value) {
      preview.value = await api.bulkUpdateMonitorTags({
        tag: from,
        remove: [from],
        add: [to],
        dry_run: true,
      })
      return
    }
    await api.bulkUpdateMonitorTags({ tag: from, remove: [from], add: [to] })
    renameOpen.value = false
    toasts.success(t('tags.renamed'))
    await load()
  } catch (error) {
    toasts.error(t('common.error'), translateError(error))
  } finally {
    applying.value = false
  }
}

onMounted(load)
</script>

<template>
  <div class="flex flex-col gap-4">
    <header class="flex flex-wrap items-center justify-between gap-3">
      <div>
        <h1 class="text-lg font-semibold">{{ t('tags.title') }}</h1>
        <p class="text-xs text-muted-foreground">{{ t('tags.subtitle') }}</p>
      </div>
      <Button variant="outline" size="sm" :loading="loading" @click="load">
        <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('common.refresh') }}
      </Button>
    </header>

    <EmptyState v-if="!tags.length" :title="t('tags.empty')" :description="t('tags.emptyHint')" />

    <Card v-else :padded="false">
      <div class="flex flex-wrap items-center gap-2 border-b border-border px-5 py-4">
        <Input v-model="tagSearch" class="max-w-xs" :placeholder="t('tags.filterPlaceholder')" />
        <span class="text-[11px] text-muted-foreground">
          {{ t('common.shownOfTotal', { shown: filteredTags.length, total: tags.length }) }}
        </span>
      </div>

      <p v-if="!filteredTags.length" class="p-5 text-xs text-muted-foreground">
        {{ t('common.noMatch') }}
      </p>

      <div v-else class="overflow-x-auto">
        <table class="data-table">
          <thead>
            <tr>
              <SortHeader
                :label="t('common.tags')"
                column="tag"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('tag')"
              />
              <SortHeader
                :label="t('tags.monitors')"
                column="monitors"
                :active="sort.key"
                :direction="sort.direction"
                @toggle="toggleSort('monitors')"
              />
              <th class="text-end">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody>
            <tr v-for="row in sortedTags" :key="row.tag">
              <td>
                <span class="flex items-center gap-2 text-sm font-medium">
                  <Tags class="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
                  <Badge variant="secondary">{{ row.tag }}</Badge>
                </span>
              </td>
              <td class="whitespace-nowrap tabular-nums">{{ row.monitors }}</td>
              <td class="text-end">
                <div class="flex items-center justify-end gap-1">
                  <Button
                    variant="ghost"
                    size="sm"
                    :title="t('tags.rename')"
                    :aria-label="t('tags.rename')"
                    @click="openRename(row)"
                  >
                    <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
                  </Button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </Card>

    <Dialog v-model="renameOpen" :title="t('tags.renameTitle')">
      <div class="grid gap-3">
        <div class="grid gap-1">
          <Label for="tag-new-name" required :help="t('tags.renameHint')">{{ t('tags.newName') }}</Label>
          <Input id="tag-new-name" v-model="newName" />
        </div>
        <p v-if="renaming" class="text-xs text-muted-foreground">
          {{ t('tags.affected', { count: renaming.monitors }) }}
        </p>
        <p
          v-if="mergeTarget"
          class="flex items-start gap-2 rounded-md border border-status-degraded/40 bg-status-degraded/10 px-3 py-2 text-xs text-status-degraded"
        >
          <TriangleAlert class="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>{{ t('tags.mergeWarning', { tag: mergeTarget.tag }) }}</span>
        </p>
        <p
          v-if="preview"
          class="rounded-md border border-border bg-muted/40 px-3 py-2 text-xs text-muted-foreground"
        >
          {{ t('tags.willUpdate', { updated: preview.updated, unchanged: preview.unchanged }) }}
        </p>
      </div>
      <template #footer>
        <Button variant="outline" @click="renameOpen = false">{{ t('common.cancel') }}</Button>
        <Button :loading="applying" @click="submit">
          {{ preview ? t('tags.apply') : t('tags.preview') }}
        </Button>
      </template>
    </Dialog>
  </div>
</template>
