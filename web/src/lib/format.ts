import type { AggregateStatus, HeartbeatSummary, HeartbeatStatus } from './types'

/** Maps a status to the Tailwind token used by the badges and bars. */
export function statusColor(status: AggregateStatus | HeartbeatStatus | 'online' | 'offline'): string {
  switch (status) {
    case 'up':
    case 'online':
      return 'bg-status-up'
    case 'down':
    case 'offline':
      return 'bg-status-down'
    case 'degraded':
      return 'bg-status-degraded'
    case 'pending':
      return 'bg-status-pending'
    case 'maintenance':
      return 'bg-status-maintenance'
    default:
      return 'bg-status-unknown'
  }
}

/** Text colour companion of statusColor (used for the status labels). */
export function statusTextColor(status: AggregateStatus | HeartbeatStatus | 'online' | 'offline'): string {
  switch (status) {
    case 'up':
    case 'online':
      return 'text-status-up'
    case 'down':
    case 'offline':
      return 'text-status-down'
    case 'degraded':
      return 'text-status-degraded'
    case 'pending':
      return 'text-status-pending'
    case 'maintenance':
      return 'text-status-maintenance'
    default:
      return 'text-status-unknown'
  }
}

/**
 * formatUptime renders an uptime percentage. A monitor without any heartbeat in
 * the window shows a dash instead of a misleading 0%.
 */
export function formatUptime(value: number, digits = 2): string {
  if (!Number.isFinite(value) || value <= 0) return '—'
  return `${value.toFixed(digits)}%`
}

/**
 * formatUptimeWindow renders the period an uptime figure covers, matching the
 * labels the backend accepts (24h, 7d, 14d, 30d).
 */
export function formatUptimeWindow(hours: number): string {
  switch (hours) {
    case 168:
      return '7d'
    case 336:
      return '14d'
    case 720:
      return '30d'
    case 24:
      return '24h'
  }
  if (hours > 0 && hours % 24 === 0) return `${hours / 24}d`
  return `${hours}h`
}

/** formatLatency renders a latency in milliseconds with the right unit. */
export function formatLatency(milliseconds: number): string {
  if (!milliseconds || milliseconds <= 0) return '—'
  if (milliseconds < 1000) return `${Math.round(milliseconds)} ms`
  return `${(milliseconds / 1000).toFixed(2)} s`
}

/**
 * The band of a Lighthouse score: 90 and above is good, 50 and above average,
 * below that poor, and `none` when the run did not measure the category. It is
 * the mirror of `models.LighthouseScoreBand`, so the API and the UI cannot
 * colour the same score differently.
 */
export type ScoreBand = 'good' | 'average' | 'poor' | 'none'

/** scoreBand classifies a score (null = not measured). */
export function scoreBand(score: number | null | undefined): ScoreBand {
  if (score === null || score === undefined) return 'none'
  if (score >= 90) return 'good'
  if (score >= 50) return 'average'
  return 'poor'
}

/** scoreColor maps a score to the Tailwind token of its band. */
export function scoreColor(score: number | null | undefined): string {
  switch (scoreBand(score)) {
    case 'good':
      return 'bg-status-up'
    case 'average':
      return 'bg-status-degraded'
    case 'poor':
      return 'bg-status-down'
    default:
      return 'bg-status-unknown/40'
  }
}

/** scoreTextColor maps a score to the Tailwind text token of its band. */
export function scoreTextColor(score: number | null | undefined): string {
  switch (scoreBand(score)) {
    case 'good':
      return 'text-status-up'
    case 'average':
      return 'text-status-degraded'
    case 'poor':
      return 'text-status-down'
    default:
      return 'text-muted-foreground'
  }
}

/**
 * formatScore renders a Lighthouse score: the number, or a dash when the
 * category was not measured (a null is "not measured", never zero).
 */
export function formatScore(score: number | null | undefined): string {
  return score === null || score === undefined ? '—' : String(Math.round(score))
}

/** formatVital renders a Core Web Vital (milliseconds, or the layout shift). */
export function formatVital(value: number, unit: 'ms' | 'cls' = 'ms'): string {
  if (unit === 'cls') {
    if (!value) return '0'
    return value < 0.01 ? value.toFixed(3) : value.toFixed(2)
  }
  if (!value) return '—'
  return value < 1000 ? `${Math.round(value)} ms` : `${(value / 1000).toFixed(2)} s`
}

/**
 * hour12Preference is the administrator clock choice (Admin > Settings). It is
 * undefined in "auto" mode, which lets Intl follow the browser locale.
 */
let hour12Preference: boolean | undefined

/** setHour12Preference applies the administrator's 12/24 hour choice. */
export function setHour12Preference(value: boolean | undefined): void {
  hour12Preference = value
}

/** hour12FromSetting maps the stored time_format to the Intl option. */
export function hour12FromSetting(value?: string): boolean | undefined {
  if (value === '12h') return true
  if (value === '24h') return false
  return undefined
}

/** formatDateTime renders an ISO timestamp using the active locale and clock. */
export function formatDateTime(value?: string | null, locale = 'en-US'): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  return new Intl.DateTimeFormat(locale, {
    dateStyle: 'medium',
    timeStyle: 'medium',
    hour12: hour12Preference,
  }).format(date)
}

/** formatRelative renders "3 minutes ago" style strings. */
export function formatRelative(value?: string | null, locale = 'en-US'): string {
  if (!value) return '—'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return '—'
  const seconds = Math.round((date.getTime() - Date.now()) / 1000)
  const formatter = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' })
  const thresholds: [Intl.RelativeTimeFormatUnit, number][] = [
    ['second', 60],
    ['minute', 60],
    ['hour', 24],
    ['day', 7],
    ['week', 4.35],
    ['month', 12],
    ['year', Number.POSITIVE_INFINITY],
  ]
  let value2 = seconds
  for (const [unit, limit] of thresholds) {
    if (Math.abs(value2) < limit) return formatter.format(Math.round(value2), unit)
    value2 /= limit
  }
  return formatter.format(Math.round(value2), 'year')
}

/** formatInterval renders "60s" / "5m" / "1h" for monitor intervals. */
export function formatInterval(seconds: number): string {
  if (!seconds || seconds <= 0) return '—'
  if (seconds < 60) return `${seconds}s`
  if (seconds % 3600 === 0) return `${seconds / 3600}h`
  if (seconds % 60 === 0) return `${seconds / 60}m`
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

/** buildBars normalises the heartbeat list used by the status bars. */
export function buildBars(heartbeats: HeartbeatSummary[] | undefined, size = 30): (HeartbeatStatus | null)[] {
  const values = (heartbeats ?? []).map((heartbeat) => heartbeat.status)
  const bars = values.slice(-size)
  while (bars.length < size) {
    bars.unshift(null as unknown as HeartbeatStatus)
  }
  return bars
}
