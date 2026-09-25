<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { Copy, ExternalLink, Pause, Pencil, Play, RefreshCw, Trash2 } from 'lucide-vue-next'
import Badge from '@/components/ui/Badge.vue'
import Button from '@/components/ui/Button.vue'
import Card from '@/components/ui/Card.vue'
import EmptyState from '@/components/ui/EmptyState.vue'
import SortHeader from '@/components/ui/SortHeader.vue'
import HeartbeatSparkline from '@/components/monitors/HeartbeatSparkline.vue'
import StatusBadge from '@/components/monitors/StatusBadge.vue'
import { certificateTitle, domainTitle, expiryStateVariant, expiryVariant } from '@/lib/expiry'
import { formatInterval, formatUptime, formatUptimeWindow } from '@/lib/format'
import type { MonitorSortState } from '@/lib/monitor-sort'
import type { MonitorSortKey } from '@/lib/sort'
import type { Monitor, MonitorGroup } from '@/lib/types'

/**
 * MonitorTable is the sortable table shared by Admin > Monitors and the
 * dashboard.
 *
 * It is presentational on purpose: the CALLER owns the filters and the sorting
 * (lib/sort.ts, lib/monitor-sort.ts) and passes the rows already filtered and
 * ordered. The component only renders the columns, so the two screens cannot
 * drift apart when a column or a badge changes.
 */
export type MonitorTableAction = 'detail' | 'edit' | 'clone' | 'check' | 'toggle' | 'remove'

const props = withDefaults(
  defineProps<{
    monitors: Monitor[]
    groups?: MonitorGroup[]
    sort: MonitorSortState
    loading?: boolean
    /** Which row actions to render (their order is fixed by the template). */
    actions?: MonitorTableAction[]
    emptyTitle?: string
    emptyDescription?: string
  }>(),
  {
    groups: () => [],
    loading: false,
    actions: () => ['edit', 'remove'] as MonitorTableAction[],
    emptyTitle: '',
    emptyDescription: '',
  },
)

const emit = defineEmits<{
  sort: [key: MonitorSortKey]
  detail: [monitor: Monitor]
  edit: [monitor: Monitor]
  clone: [monitor: Monitor]
  check: [monitor: Monitor]
  toggle: [monitor: Monitor]
  remove: [monitor: Monitor]
}>()

const { t, locale } = useI18n()

const groupNames = computed(() => new Map(props.groups.map((group) => [group.id, group.name])))

/** hasCertificates reveals the validity column only when it says something. */
const hasCertificates = computed(() => props.monitors.some((monitor) => monitor.cert_watch || monitor.certificate))

/** hasDomains reveals the domain expiration column on the same rule. */
const hasDomains = computed(() => props.monitors.some((monitor) => monitor.domain_watch || monitor.domain))

/** hasHeartbeats reveals the sparkline column only when at least one bar exists. */
const hasHeartbeats = computed(() => props.monitors.some((monitor) => (monitor.heartbeat_bars ?? []).length > 0))

const showActions = computed(() => props.actions.length > 0)

function shows(action: MonitorTableAction): boolean {
  return props.actions.includes(action)
}
</script>

<template>
  <EmptyState
    v-if="!props.loading && !props.monitors.length"
    :title="props.emptyTitle"
    :description="props.emptyDescription"
  >
    <slot name="empty" />
  </EmptyState>

  <Card v-else :padded="false">
    <div class="overflow-x-auto">
      <table class="data-table">
        <thead>
          <tr>
            <SortHeader
              :label="t('common.name')"
              column="name"
              :active="props.sort.key"
              :direction="props.sort.direction"
              @toggle="emit('sort', 'name')"
            />
            <SortHeader
              :label="t('common.type')"
              column="type"
              :active="props.sort.key"
              :direction="props.sort.direction"
              @toggle="emit('sort', 'type')"
            />
            <SortHeader
              :label="t('monitor.groupsSection')"
              column="group"
              :active="props.sort.key"
              :direction="props.sort.direction"
              @toggle="emit('sort', 'group')"
            />
            <SortHeader
              v-if="hasCertificates"
              :label="t('certificate.column')"
              column="certificate"
              :active="props.sort.key"
              :direction="props.sort.direction"
              @toggle="emit('sort', 'certificate')"
            />
            <SortHeader
              v-if="hasDomains"
              :label="t('domain.column')"
              column="domain"
              :active="props.sort.key"
              :direction="props.sort.direction"
              @toggle="emit('sort', 'domain')"
            />
            <SortHeader
              :label="t('common.status')"
              column="status"
              :active="props.sort.key"
              :direction="props.sort.direction"
              @toggle="emit('sort', 'status')"
            />
            <SortHeader
              :label="t('common.interval')"
              column="interval"
              :active="props.sort.key"
              :direction="props.sort.direction"
              @toggle="emit('sort', 'interval')"
            />
            <th v-if="hasHeartbeats">{{ t('monitor.heartbeat') }}</th>
            <SortHeader
              :label="t('common.uptime')"
              column="uptime"
              :active="props.sort.key"
              :direction="props.sort.direction"
              @toggle="emit('sort', 'uptime')"
            />
            <th v-if="showActions">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="monitor in props.monitors" :key="monitor.id">
            <td>
              <button class="text-start hover:underline" @click="emit('detail', monitor)">
                {{ monitor.name }}
              </button>
              <Badge v-if="monitor.template_name" variant="outline" class="ms-1">
                {{ monitor.template_name }}
              </Badge>
            </td>
            <td><Badge variant="secondary">{{ monitor.type }}</Badge></td>
            <td>
              <div class="flex flex-wrap gap-1">
                <Badge v-for="id in monitor.group_ids ?? []" :key="id" variant="outline">
                  {{ groupNames.get(id) ?? id }}
                </Badge>
                <span v-if="!(monitor.group_ids ?? []).length" class="text-muted-foreground">—</span>
              </div>
            </td>
            <td v-if="hasCertificates">
              <Badge
                v-if="monitor.certificate"
                :variant="expiryVariant(monitor.certificate.days_left)"
                :title="certificateTitle(monitor.certificate, locale)"
              >
                {{ t('certificate.daysLeft', { days: monitor.certificate.days_left }) }}
              </Badge>
              <Badge v-else-if="monitor.cert_watch" variant="secondary">{{ t('certificate.pending') }}</Badge>
              <span v-else class="text-muted-foreground">—</span>
            </td>
            <td v-if="hasDomains">
              <Badge
                v-if="monitor.domain && monitor.domain.status === 'ok'"
                :variant="expiryStateVariant(monitor.domain.status, monitor.domain.days_left)"
                :title="domainTitle(monitor.domain, locale)"
              >
                {{ t('domain.daysLeft', { days: monitor.domain.days_left }) }}
              </Badge>
              <Badge v-else-if="monitor.domain && monitor.domain.status === 'not_found'" variant="secondary">
                {{ t('domain.notFound') }}
              </Badge>
              <Badge v-else-if="monitor.domain && monitor.domain.status === 'unsupported'" variant="secondary">
                {{ t('domain.unsupported') }}
              </Badge>
              <Badge v-else-if="monitor.domain" variant="secondary" :title="monitor.domain.error || ''">
                {{ t('domain.unavailable') }}
              </Badge>
              <Badge v-else-if="monitor.domain_watch" variant="secondary">{{ t('domain.pending') }}</Badge>
              <span v-else class="text-muted-foreground">—</span>
            </td>
            <td><StatusBadge :status="monitor.status" /></td>
            <td>{{ formatInterval(monitor.interval_seconds) }}</td>
            <td v-if="hasHeartbeats">
              <HeartbeatSparkline :bars="monitor.heartbeat_bars" />
            </td>
            <td>
              <span class="tabular-nums">{{ formatUptime(monitor.uptime) }}</span>
              <span class="ms-1 text-[10px] text-muted-foreground">{{ formatUptimeWindow(monitor.uptime_hours) }}</span>
            </td>
            <td v-if="showActions">
              <div class="flex items-center gap-1">
                <Button
                  v-if="shows('detail')"
                  variant="ghost"
                  size="sm"
                  :title="t('monitor.detail')"
                  @click="emit('detail', monitor)"
                >
                  <ExternalLink class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button
                  v-if="shows('check')"
                  variant="ghost"
                  size="sm"
                  :title="t('dashboard.checkNow')"
                  @click="emit('check', monitor)"
                >
                  <RefreshCw class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button
                  v-if="shows('toggle')"
                  variant="ghost"
                  size="sm"
                  :title="monitor.active ? t('monitor.pauseTitle') : t('common.active')"
                  @click="emit('toggle', monitor)"
                >
                  <Play v-if="!monitor.active" class="h-3.5 w-3.5" aria-hidden="true" />
                  <Pause v-else class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button v-if="shows('edit')" variant="ghost" size="sm" :title="t('common.edit')" @click="emit('edit', monitor)">
                  <Pencil class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button v-if="shows('clone')" variant="ghost" size="sm" :title="t('common.clone')" @click="emit('clone', monitor)">
                  <Copy class="h-3.5 w-3.5" aria-hidden="true" />
                </Button>
                <Button
                  v-if="shows('remove')"
                  variant="ghost"
                  size="sm"
                  class="text-status-down"
                  :title="t('common.delete')"
                  @click="emit('remove', monitor)"
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
</template>
