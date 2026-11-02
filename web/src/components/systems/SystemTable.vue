<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import { BellRing, KeyRound, Pencil, Plus, Trash2 } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SortHeader from '@/components/ui/SortHeader.vue'
import { formatDateTime, formatDuration, formatPercent } from '@/lib/format'
import type { SystemSortKey, SystemSortState } from '@/lib/system-sort'
import { systemFiringMetrics } from '@/lib/system-sort'
import type { System, SystemPointOverview } from '@/lib/types-systems'

/**
 * SystemTable is the table of the Systems page: one row per machine.
 *
 * It is presentational on purpose, exactly like MonitorTable: the CALLER owns the
 * filters (the search box and the summary boxes) and the sort
 * (lib/system-sort.ts) and passes the rows already filtered and ordered, so a
 * column changes here once. The counters of a row are not on the system: they are
 * the latest sample, which the caller passes in its own map, keyed by system id.
 */
const props = withDefaults(
  defineProps<{
    systems: System[]
    sort: SystemSortState
    overview: Map<number, SystemPointOverview>
    loading?: boolean
    emptyTitle?: string
    emptyDescription?: string
  }>(),
  { loading: false, emptyTitle: '', emptyDescription: '' },
)

const emit = defineEmits<{
  sort: [key: SystemSortKey]
  edit: [system: System]
  remove: [system: System]
  rotate: [system: System]
  alerts: [system: System]
  create: []
}>()

const { t, locale } = useI18n()

/** sampleOf is the last sample of a machine (undefined while there is none). */
function sampleOf(id: number): SystemPointOverview | undefined {
  return props.overview.get(id)
}

/** statusVariant maps a system status to the badge colour. */
function statusVariant(status: System['status']): 'success' | 'danger' | 'warning' {
  if (status === 'up') return 'success'
  if (status === 'down') return 'danger'
  return 'warning'
}

/** firingMetrics is the list of rules firing on a machine, as one label. */
function firingMetrics(system: System): string {
  return systemFiringMetrics(system)
    .map((metric) => t(`systems.metric.${metric}`))
    .join(', ')
}

const hasRows = computed(() => props.systems.length > 0)
</script>

<template>
  <Card :padded="false">
    <EmptyState
      v-if="!loading && !hasRows"
      :title="emptyTitle || t('systems.emptyTitle')"
      :description="emptyDescription || t('systems.emptyDescription')"
    >
      <Button size="sm" @click="emit('create')">
        <Plus class="h-3.5 w-3.5" aria-hidden="true" />
        {{ t('systems.addSystem') }}
      </Button>
    </EmptyState>

    <div v-else class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="border-b border-border text-start text-[11px] uppercase tracking-wide text-muted-foreground">
          <tr>
            <SortHeader
              :label="t('systems.system')"
              column="name"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="emit('sort', 'name')"
            />
            <SortHeader
              :label="t('common.status')"
              column="status"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="emit('sort', 'status')"
            />
            <SortHeader
              label="CPU"
              column="cpu"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="emit('sort', 'cpu')"
            />
            <SortHeader
              :label="t('systems.memory')"
              column="memory"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="emit('sort', 'memory')"
            />
            <SortHeader
              :label="t('systems.disk')"
              column="disk"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="emit('sort', 'disk')"
            />
            <SortHeader
              :label="t('systems.containersLabel')"
              column="containers"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="emit('sort', 'containers')"
            />
            <SortHeader
              :label="t('systems.uptimeColumn')"
              column="uptime"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="emit('sort', 'uptime')"
            />
            <SortHeader
              :label="t('systems.lastSampleColumn')"
              column="sample"
              :active="sort.key"
              :direction="sort.direction"
              @toggle="emit('sort', 'sample')"
            />
            <th class="px-4 py-2 text-start font-medium">{{ t('systems.owner') }}</th>
            <th class="px-4 py-2 text-end font-medium">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr
            v-for="system in systems"
            :key="system.id"
            class="border-b border-border/60 align-top last:border-0"
          >
            <td class="px-4 py-2">
              <div class="flex flex-wrap items-center gap-1.5">
                <RouterLink
                  :to="{ name: 'system-detail', params: { id: system.id } }"
                  class="font-medium hover:underline"
                >
                  {{ system.name }}
                </RouterLink>
                <Badge v-if="firingMetrics(system)" variant="danger" :title="firingMetrics(system)">
                  <BellRing class="h-3 w-3" aria-hidden="true" />
                  {{ firingMetrics(system) }}
                </Badge>
              </div>
              <p class="mt-0.5 text-[11px] text-muted-foreground">
                {{ system.hostname || system.host || t('systems.noHost') }}
                <span v-if="system.os_name"> · {{ system.os_name }}</span>
                <span v-if="system.agent_version"> · agent {{ system.agent_version }}</span>
              </p>
              <p v-if="system.last_error" class="text-[11px] text-destructive">{{ system.last_error }}</p>
            </td>
            <td class="px-4 py-2">
              <Badge :variant="statusVariant(system.status)">{{ t(`status.${system.status}`) }}</Badge>
            </td>
            <td class="px-4 py-2 tabular-nums">{{ formatPercent(sampleOf(system.id)?.cpu) }}</td>
            <td class="px-4 py-2 tabular-nums">{{ formatPercent(sampleOf(system.id)?.mem_pct) }}</td>
            <td class="px-4 py-2 tabular-nums">{{ formatPercent(sampleOf(system.id)?.disk_pct) }}</td>
            <td class="px-4 py-2 tabular-nums">{{ sampleOf(system.id)?.containers ?? 0 }}</td>
            <td class="px-4 py-2 tabular-nums">{{ formatDuration(system.uptime) }}</td>
            <td class="px-4 py-2 text-xs text-muted-foreground">
              <template v-if="system.last_sample_at">
                {{ formatDateTime(system.last_sample_at, locale) }}
              </template>
              <template v-else>{{ t('systems.noSampleYet') }}</template>
            </td>
            <td class="px-4 py-2 text-xs text-muted-foreground">
              {{ system.owner_node_name || '—' }}
            </td>
            <td class="px-4 py-2">
              <div class="flex items-center justify-end gap-1">
                <Button
                  variant="ghost"
                  size="icon"
                  :aria-label="t('systems.alertsButton')"
                  @click="emit('alerts', system)"
                >
                  <BellRing class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button variant="ghost" size="icon" :aria-label="t('common.edit')" @click="emit('edit', system)">
                  <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button
                  variant="ghost"
                  size="icon"
                  :aria-label="t('systems.rotateToken')"
                  @click="emit('rotate', system)"
                >
                  <KeyRound class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button variant="ghost" size="icon" :aria-label="t('common.delete')" @click="emit('remove', system)">
                  <Trash2 class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </Card>
</template>

