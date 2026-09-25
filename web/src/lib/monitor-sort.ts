import type { MonitorSortKey, SortDirection } from './sort'
import { monitorSortKeys } from './sort'

/**
 * State of the monitors tables (dashboard and Admin > Monitors).
 *
 * The choice is remembered per browser, so an operator who always sorts by
 * "domain" finds the table that way after a reload. A hand edited or stale
 * value falls back to the default instead of breaking the page.
 */

/** The key the tables remember their sort in. */
export const monitorSortStorageKey = 'up.admin.monitors.sort'

export interface MonitorSortState {
  key: MonitorSortKey
  direction: SortDirection
}

/** The default order: by name, ascending. */
export const defaultMonitorSort: MonitorSortState = { key: 'name', direction: 'asc' }

/** loadMonitorSort reads the stored state, falling back to the default. */
export function loadMonitorSort(): MonitorSortState {
  try {
    const raw = localStorage.getItem(monitorSortStorageKey)
    if (!raw) return { ...defaultMonitorSort }
    const parsed = JSON.parse(raw) as { key?: MonitorSortKey; direction?: SortDirection }
    if (!parsed.key || !monitorSortKeys.includes(parsed.key)) return { ...defaultMonitorSort }
    return { key: parsed.key, direction: parsed.direction === 'desc' ? 'desc' : 'asc' }
  } catch {
    return { ...defaultMonitorSort }
  }
}

/**
 * toggleMonitorSort switches the column, or flips the direction of the current
 * one, and remembers the result. A browser that refuses to persist (private
 * mode) still sorts, it just forgets the preference after a reload.
 */
export function toggleMonitorSort(current: MonitorSortState, key: MonitorSortKey): MonitorSortState {
  const next: MonitorSortState =
    current.key === key
      ? { key, direction: current.direction === 'asc' ? 'desc' : 'asc' }
      : { key, direction: 'asc' }
  try {
    localStorage.setItem(monitorSortStorageKey, JSON.stringify(next))
  } catch {
    // Ignored on purpose: sorting must not depend on storage being available.
  }
  return next
}