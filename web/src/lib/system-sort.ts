import type { SortDirection } from './sort'
import { loadTableSort, toggleTableSort } from './table-sort'
import type { TableSortState } from './table-sort'
import type { System, SystemAlertMetric, SystemAlertRule, SystemPointOverview } from './types-systems'

/**
 * Sorting and alert helpers of the Systems table.
 *
 * The comparators live here rather than in lib/sort.ts because the rows are a
 * different shape: the numbers a machine is sorted by (CPU, memory, disk,
 * containers) are NOT on the system row — they are the latest sample, which the
 * page holds in its own map. Passing that map in keeps the comparators pure, the
 * same way lib/sort.ts passes the locale and the group names.
 *
 * Uptime is the exception: it is denormalised onto the system row (models.System),
 * so it is read from the machine itself and needs no sample.
 */

/** The columns of the table an operator can sort by. */
export type SystemSortKey =
  | 'name'
  | 'status'
  | 'cpu'
  | 'memory'
  | 'disk'
  | 'containers'
  | 'uptime'
  | 'sample'

/** The columns accepted by loadSystemSort (guards a hand edited value). */
export const systemSortKeys: SystemSortKey[] = [
  'name',
  'status',
  'cpu',
  'memory',
  'disk',
  'containers',
  'uptime',
  'sample',
]

/** The key the table remembers its sort in. */
export const systemSortStorageKey = 'up.systems.sort'

export type SystemSortState = TableSortState<SystemSortKey>

/** The default order: by name, ascending. */
export const defaultSystemSort: SystemSortState = { key: 'name', direction: 'asc' }

/** What the comparators need from the view (passed in to keep them pure). */
export interface SystemSortContext {
  locale: string
  overview: Map<number, SystemPointOverview>
}

/**
 * statusRank orders the statuses by the attention they deserve, exactly like the
 * monitors table: sorting ascending puts the failures first.
 */
const statusRank: Record<string, number> = { down: 0, pending: 1, up: 2 }

/** sampleOf is the latest sample of a machine (undefined while there is none). */
export function sampleOf(context: SystemSortContext, id: number): SystemPointOverview | undefined {
  return context.overview.get(id)
}

/** compareSystems orders two rows for a column. */
export function compareSystems(
  a: System,
  b: System,
  key: SystemSortKey,
  direction: SortDirection,
  context: SystemSortContext,
): number {
  const sign = direction === 'asc' ? 1 : -1
  const byName = () => a.name.localeCompare(b.name, context.locale) || a.id - b.id
  const left = sampleOf(context, a.id)
  const right = sampleOf(context, b.id)

  switch (key) {
    case 'status':
      return sign * ((statusRank[a.status] ?? 9) - (statusRank[b.status] ?? 9)) || byName()
    case 'cpu':
      return sign * ((left?.cpu ?? -1) - (right?.cpu ?? -1)) || byName()
    case 'memory':
      return sign * ((left?.mem_pct ?? -1) - (right?.mem_pct ?? -1)) || byName()
    case 'disk':
      return sign * ((left?.disk_pct ?? -1) - (right?.disk_pct ?? -1)) || byName()
    case 'containers':
      return sign * ((left?.containers ?? -1) - (right?.containers ?? -1)) || byName()
    case 'uptime':
      // It is on the row, not in the sample: a machine that never reported sorts
      // as the lowest uptime, exactly like a missing sample above.
      return sign * ((a.uptime || -1) - (b.uptime || -1)) || byName()
    case 'sample':
      return sign * (sampleTime(a) - sampleTime(b)) || byName()
    default:
      return sign * a.name.localeCompare(b.name, context.locale) || a.id - b.id
  }
}

/** sampleTime is the epoch of the last sample (0 when the machine never reported). */
function sampleTime(system: System): number {
  if (!system.last_sample_at) return 0
  const parsed = Date.parse(system.last_sample_at)
  return Number.isNaN(parsed) ? 0 : parsed
}

/** sortSystems returns a sorted copy (the list the store holds is not mutated). */
export function sortSystems(
  systems: System[],
  key: SystemSortKey,
  direction: SortDirection,
  context: SystemSortContext,
): System[] {
  return [...systems].sort((a, b) => compareSystems(a, b, key, direction, context))
}

/** loadSystemSort reads the stored state, falling back to the default. */
export function loadSystemSort(): SystemSortState {
  return loadTableSort({
    storageKey: systemSortStorageKey,
    keys: systemSortKeys,
    defaultKey: defaultSystemSort.key,
    defaultDirection: defaultSystemSort.direction,
  })
}

/** toggleSystemSort switches the column, or flips the direction of the current one. */
export function toggleSystemSort(current: SystemSortState, key: SystemSortKey): SystemSortState {
  return toggleTableSort(current, key, systemSortStorageKey)
}

/**
 * systemAlertRule returns the rule of one metric, with the server default when the
 * payload does not carry it (an older node, or a machine created before the alert
 * rules existed).
 */
export function systemAlertRule(system: System, metric: SystemAlertMetric): SystemAlertRule {
  const found = (system.alerts ?? []).find((rule) => rule.metric === metric)
  if (found) return found
  return {
    metric,
    enabled: true,
    threshold: metric === 'disk' ? 85 : 80,
    min_minutes: 5,
    triggered: false,
  }
}

/** highMetric is the latest value of the metric a rule watches. */
export function highMetric(sample: SystemPointOverview | undefined, metric: SystemAlertMetric): number {
  if (!sample) return 0
  switch (metric) {
    case 'cpu':
      return sample.cpu
    case 'memory':
      return sample.mem_pct
    case 'disk':
      return sample.disk_pct
    default:
      return 0
  }
}

/**
 * systemOverThreshold reports whether a machine is over the threshold of one
 * metric right now, which is what the "High CPU/memory/disk" boxes count.
 *
 * It reads the RULE the operator configured (not a fixed number) and its enabled
 * switch, so a box never lights up for a rule that cannot fire.
 */
export function systemOverThreshold(
  system: System,
  metric: SystemAlertMetric,
  overview: Map<number, SystemPointOverview>,
): boolean {
  const rule = systemAlertRule(system, metric)
  if (!rule.enabled) return false
  return highMetric(overview.get(system.id), metric) >= rule.threshold
}

/** systemAlerting reports whether any rule of the machine is firing. */
export function systemAlerting(system: System): boolean {
  return (system.alerts ?? []).some((rule) => rule.triggered)
}

/** systemFiringMetrics lists the metrics that are firing (for the row badge). */
export function systemFiringMetrics(system: System): SystemAlertMetric[] {
  return (system.alerts ?? []).filter((rule) => rule.triggered).map((rule) => rule.metric)
}

