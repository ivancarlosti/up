import type { MonitorSortKey } from './sort'
import { monitorSortKeys } from './sort'
import { loadTableSort, toggleTableSort } from './table-sort'
import type { TableSortState } from './table-sort'

/**
 * State of the dashboard monitors table.
 *
 * The dashboard is the one monitors screen since 2026-09-27, so there is a single
 * preference to remember. The rules of the remembered state (validation,
 * fallback, persistence) live in `lib/table-sort.ts` with the other admin tables.
 */

/** The key the tables remember their sort in. */
export const monitorSortStorageKey = 'up.admin.monitors.sort'

export type MonitorSortState = TableSortState<MonitorSortKey>

/** The default order: by name, ascending. */
export const defaultMonitorSort: MonitorSortState = { key: 'name', direction: 'asc' }

/** loadMonitorSort reads the stored state, falling back to the default. */
export function loadMonitorSort(): MonitorSortState {
  return loadTableSort({
    storageKey: monitorSortStorageKey,
    keys: monitorSortKeys,
    defaultKey: defaultMonitorSort.key,
    defaultDirection: defaultMonitorSort.direction,
  })
}

/** toggleMonitorSort switches the column, or flips the direction of the current one. */
export function toggleMonitorSort(current: MonitorSortState, key: MonitorSortKey): MonitorSortState {
  return toggleTableSort(current, key, monitorSortStorageKey)
}
