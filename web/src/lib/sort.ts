import type { Monitor } from './types'

/**
 * Sorting of the admin monitors table.
 *
 * The comparator lives here, outside the view, so the rules are explicit and the
 * table only owns the interaction: rows without a value (no group, no expiry
 * date) always sink to the bottom, and every comparison falls back to the name so
 * equal values never swap places between renders.
 */

/** The columns an operator can sort by. */
export type MonitorSortKey =
  | 'name'
  | 'type'
  | 'group'
  | 'certificate'
  | 'domain'
  | 'status'
  | 'interval'
  | 'uptime'

/** The two directions of a sortable column. */
export type SortDirection = 'asc' | 'desc'

/** What the comparator needs from the view (passed in to keep it pure). */
export interface MonitorSortContext {
  groupNames: Map<number, string>
  locale: string
}

/** The columns accepted by loadSort (guards a hand edited localStorage value). */
export const monitorSortKeys: MonitorSortKey[] = [
  'name',
  'type',
  'group',
  'certificate',
  'domain',
  'status',
  'interval',
  'uptime',
]

/**
 * statusRank orders the statuses by the attention they deserve: sorting
 * ascending puts the failures first and the healthy monitors last.
 */
const statusRank: Record<string, number> = {
  down: 0,
  degraded: 1,
  pending: 2,
  unknown: 3,
  maintenance: 4,
  up: 5,
}

/** pausedRank keeps the paused monitors at the bottom in both directions. */
const pausedRank = 99

/** rank maps a monitor to its status order. */
function rank(monitor: Monitor): number {
  if (!monitor.active) return pausedRank
  return statusRank[monitor.status] ?? 50
}

/** expiryDaysOf reads the days left shown in the expiry column (null when none). */
function expiryDaysOf(monitor: Monitor, key: 'certificate' | 'domain'): number | null {
  if (key === 'certificate') {
    return typeof monitor.certificate?.days_left === 'number' ? monitor.certificate.days_left : null
  }
  if (monitor.domain?.status !== 'ok') return null
  return typeof monitor.domain.days_left === 'number' ? monitor.domain.days_left : null
}

/** groupLabelOf is the joined group names of a monitor ("" when ungrouped). */
function groupLabelOf(monitor: Monitor, context: MonitorSortContext): string {
  return (monitor.group_ids ?? [])
    .map((id) => context.groupNames.get(id) ?? '')
    .filter(Boolean)
    .sort((a, b) => a.localeCompare(b, context.locale))
    .join(', ')
}

/** compareMonitors orders two rows for a column. */
export function compareMonitors(
  a: Monitor,
  b: Monitor,
  key: MonitorSortKey,
  direction: SortDirection,
  context: MonitorSortContext,
): number {
  const sign = direction === 'asc' ? 1 : -1
  const byName = () => a.name.localeCompare(b.name, context.locale)

  switch (key) {
    case 'name':
      return sign * byName() || a.id - b.id
    case 'type':
      return sign * a.type.localeCompare(b.type, context.locale) || byName()
    case 'group': {
      const left = groupLabelOf(a, context)
      const right = groupLabelOf(b, context)
      if (!left && !right) return byName()
      if (!left) return 1
      if (!right) return -1
      return sign * left.localeCompare(right, context.locale) || byName()
    }
    case 'status':
      return sign * (rank(a) - rank(b)) || byName()
    case 'interval':
      return sign * (a.interval_seconds - b.interval_seconds) || byName()
    case 'uptime':
      return sign * ((a.uptime_24h ?? 0) - (b.uptime_24h ?? 0)) || byName()
    case 'certificate':
    case 'domain': {
      const left = expiryDaysOf(a, key)
      const right = expiryDaysOf(b, key)
      if (left === null && right === null) return byName()
      if (left === null) return 1
      if (right === null) return -1
      return sign * (left - right) || byName()
    }
    default:
      return byName()
  }
}

/** sortMonitors returns a sorted copy (the list a view holds is not mutated). */
export function sortMonitors(
  monitors: Monitor[],
  key: MonitorSortKey,
  direction: SortDirection,
  context: MonitorSortContext,
): Monitor[] {
  return [...monitors].sort((a, b) => compareMonitors(a, b, key, direction, context))
}
