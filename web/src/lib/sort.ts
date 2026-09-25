import type { ExpiryTarget, Monitor } from './types'

/**
 * Sorting of the admin tables.
 *
 * The comparators live here, outside the views, so the rules are explicit and a
 * table only owns the interaction: rows without a value (no group, no expiry
 * date, never checked) always sink to the bottom, and every comparison falls
 * back to a stable key so equal values never swap places between renders.
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

/**
 * The columns of the "targets of the next run" table an operator can sort by.
 */
export type ExpiryTargetSortKey = 'kind' | 'label' | 'monitors' | 'status' | 'checked'

/** The columns accepted by the expiry table (guards a stale localStorage value). */
export const expiryTargetSortKeys: ExpiryTargetSortKey[] = ['kind', 'label', 'monitors', 'status', 'checked']

/** What the expiry comparator needs from the view (passed in to keep it pure). */
export interface ExpirySortContext {
  locale: string
}

/**
 * expiryStatusRank orders the statuses by the attention they deserve: sorting
 * ascending puts the targets that need a look first (an error, a TLD without a
 * parser, a domain that is gone) and the healthy ones last. A target that was
 * never checked sinks below even the healthy ones.
 */
const expiryStatusRank: Record<string, number> = {
  error: 0,
  unsupported: 1,
  not_found: 2,
  ok: 3,
}

/** expiryStatusOf maps a target to its rank (empty status = never checked). */
function expiryStatusOf(target: ExpiryTarget): number {
  if (target.status && target.status in expiryStatusRank) return expiryStatusRank[target.status]
  return 99
}

/** targetDaysLeftOf reads the days left shown for the target (null when there is none). */
function targetDaysLeftOf(target: ExpiryTarget): number | null {
  return typeof target.days_left === 'number' ? target.days_left : null
}

/** expiryTimestampOf parses the last-check timestamp (NaN when never checked). */
function expiryTimestampOf(target: ExpiryTarget): number {
  return target.checked_at ? new Date(target.checked_at).getTime() : Number.NaN
}

/** compareExpiryTargets orders two rows for a column. */
export function compareExpiryTargets(
  a: ExpiryTarget,
  b: ExpiryTarget,
  key: ExpiryTargetSortKey,
  direction: SortDirection,
  context: ExpirySortContext,
): number {
  const sign = direction === 'asc' ? 1 : -1
  const byKey = () => a.key.localeCompare(b.key, context.locale)

  switch (key) {
    case 'kind':
      return sign * a.kind.localeCompare(b.kind, context.locale) || byKey()
    case 'label':
      return sign * a.label.localeCompare(b.label, context.locale) || byKey()
    case 'monitors':
      return sign * (a.monitors.length - b.monitors.length) || byKey()
    case 'status': {
      const left = expiryStatusOf(a)
      const right = expiryStatusOf(b)
      if (left !== right) return sign * (left - right)
      // Within one rank the most urgent date comes first, regardless of the
      // direction of the status column (a target with fewer days left is more
      // interesting than one with more).
      const leftDays = targetDaysLeftOf(a)
      const rightDays = targetDaysLeftOf(b)
      if (leftDays !== null && rightDays !== null && leftDays !== rightDays) return leftDays - rightDays
      return byKey()
    }
    case 'checked': {
      const left = expiryTimestampOf(a)
      const right = expiryTimestampOf(b)
      const leftEmpty = Number.isNaN(left)
      const rightEmpty = Number.isNaN(right)
      if (leftEmpty && rightEmpty) return byKey()
      if (leftEmpty) return 1
      if (rightEmpty) return -1
      return sign * (left - right) || byKey()
    }
    default:
      return byKey()
  }
}

/** sortExpiryTargets returns a sorted copy (the list a view holds is not mutated). */
export function sortExpiryTargets(
  targets: ExpiryTarget[],
  key: ExpiryTargetSortKey,
  direction: SortDirection,
  context: ExpirySortContext,
): ExpiryTarget[] {
  return [...targets].sort((a, b) => compareExpiryTargets(a, b, key, direction, context))
}
