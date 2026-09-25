import { formatUptimeWindow } from './format'

/**
 * The uptime windows the UI offers (24 h, 7 d, 14 d and 30 d).
 *
 * It mirrors models.UptimeWindowHoursAllowed on the Go side: the backend clamps
 * an unknown value to the closest one of these, so the two lists must agree.
 */
export const uptimeWindows = [24, 168, 336, 720] as const

/** uptimeWindowOptions builds the {value,label} list of a Select. */
export function uptimeWindowOptions(): { value: string; label: string }[] {
  return uptimeWindows.map((hours) => ({ value: String(hours), label: formatUptimeWindow(hours) }))
}

/**
 * uptimeWindowOptionsWithInherit adds the "inherit the global window" entry
 * (value 0) used by the status pages.
 */
export function uptimeWindowOptionsWithInherit(inheritLabel: string): { value: string; label: string }[] {
  return [{ value: '0', label: inheritLabel }, ...uptimeWindowOptions()]
}
