import type { SortDirection } from './sort'

/**
 * Sort state of the admin tables.
 *
 * Every sortable table remembers its column per browser, so an operator who
 * always sorts by "status" finds the table that way after a reload. The rules
 * live here once, instead of being copied into each view: a hand edited or stale
 * value falls back to the default instead of breaking the page, and a browser
 * that refuses to persist (private mode) still sorts, it just forgets the
 * preference after a reload.
 *
 * The comparators are NOT here: they belong to the data (`lib/sort.ts`), so a
 * table only owns the interaction and the storage.
 */

/** The column and direction a table is currently sorted by. */
export interface TableSortState<K extends string> {
  key: K
  direction: SortDirection
}

/** How a table describes its own preference. */
export interface TableSortOptions<K extends string> {
  /** The localStorage key the table remembers the choice in. */
  storageKey: string
  /** The accepted columns, guarding a stale or hand edited value. */
  keys: readonly K[]
  /** The column used when nothing usable was remembered. */
  defaultKey: K
  /** The direction of defaultKey (ascending when omitted). */
  defaultDirection?: SortDirection
}

/** loadTableSort reads the stored state, falling back to the default. */
export function loadTableSort<K extends string>(options: TableSortOptions<K>): TableSortState<K> {
  const fallback: TableSortState<K> = {
    key: options.defaultKey,
    direction: options.defaultDirection ?? 'asc',
  }
  try {
    const raw = localStorage.getItem(options.storageKey)
    if (!raw) return fallback
    const parsed = JSON.parse(raw) as { key?: string; direction?: string }
    if (!parsed.key || !options.keys.includes(parsed.key as K)) return fallback
    return { key: parsed.key as K, direction: parsed.direction === 'desc' ? 'desc' : 'asc' }
  } catch {
    return fallback
  }
}

/**
 * toggleTableSort switches the column, or flips the direction of the current
 * one, and remembers the result under the key the table owns.
 */
export function toggleTableSort<K extends string>(
  current: TableSortState<K>,
  key: K,
  storageKey: string,
): TableSortState<K> {
  const next: TableSortState<K> =
    current.key === key
      ? { key, direction: current.direction === 'asc' ? 'desc' : 'asc' }
      : { key, direction: 'asc' }
  try {
    localStorage.setItem(storageKey, JSON.stringify(next))
  } catch {
    // Ignored on purpose: sorting must not depend on storage being available.
  }
  return next
}
