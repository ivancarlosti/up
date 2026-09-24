<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { ChevronDown, ChevronUp, ChevronsUpDown } from 'lucide-vue-next'
import type { SortDirection } from '@/lib/sort'

/**
 * A sortable table header: it renders the <th> itself (so the table markup stays
 * valid), keeps the sort state in the parent and announces the order to screen
 * readers through aria-sort.
 */
const props = defineProps<{
  /** What the column is called. */
  label: string
  /** The key this header sorts by. */
  column: string
  /** The key the table is currently sorted by. */
  active: string
  /** The current direction (ignored when the column is not active). */
  direction: SortDirection
}>()

const emit = defineEmits<{ toggle: [] }>()

const { t } = useI18n()

const isActive = computed(() => props.active === props.column)
const ariaSort = computed<'ascending' | 'descending' | 'none'>(() => {
  if (!isActive.value) return 'none'
  return props.direction === 'asc' ? 'ascending' : 'descending'
})
const help = computed(() =>
  isActive.value
    ? t(props.direction === 'asc' ? 'common.sortedAscending' : 'common.sortedDescending')
    : t('common.sortBy', { column: props.label }),
)
</script>

<template>
  <th :aria-sort="ariaSort">
    <button
      type="button"
      class="inline-flex items-center gap-1 font-medium hover:text-foreground"
      :title="help"
      @click="emit('toggle')"
    >
      {{ label }}
      <ChevronUp v-if="isActive && direction === 'asc'" class="h-3 w-3" aria-hidden="true" />
      <ChevronDown v-else-if="isActive" class="h-3 w-3" aria-hidden="true" />
      <ChevronsUpDown v-else class="h-3 w-3 opacity-40" aria-hidden="true" />
    </button>
  </th>
</template>
