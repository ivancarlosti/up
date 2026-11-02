import type { SystemAlertMetric, SystemAlertRuleConfig, SystemAlertSpec } from './types-systems'

/**
 * The alert rules forms: the machine dialog and the Admin > Settings card edit
 * the same shape, so the conversion lives here instead of being copied twice.
 *
 * A form keeps the numbers as strings because that is what the inputs speak; the
 * API payload is built from them on save.
 */

/** One rule as the editor holds it. */
export interface AlertRuleForm {
  metric: SystemAlertMetric
  enabled: boolean
  threshold: string
  minutes: string
}

/** ruleFormOf turns a stored rule into the shape of the editor. */
export function ruleFormOf(rule: SystemAlertRuleConfig): AlertRuleForm {
  return {
    metric: rule.metric,
    enabled: rule.enabled,
    threshold: String(rule.threshold),
    minutes: String(rule.min_minutes),
  }
}

/** rulePayloadOf turns the editor back into the rule the API stores. */
export function rulePayloadOf(rule: AlertRuleForm): SystemAlertRuleConfig {
  return {
    metric: rule.metric,
    enabled: rule.enabled,
    threshold: Number(rule.threshold) || 0,
    min_minutes: Number(rule.minutes) || 0,
  }
}

/**
 * unitSuffix is the symbol shown next to a threshold ("%", "°C", "MB/s").
 *
 * The word units ("load", "containers", "disks") are not appended: "10 load" is
 * noise, and the rule already names what it counts.
 */
export function unitSuffix(spec: SystemAlertSpec | undefined): string {
  if (!spec) return ''
  return spec.unit === '%' || spec.unit === '°C' || spec.unit === 'MB/s' ? spec.unit : ''
}

/**
 * defaultOf returns the value a reset would restore for one rule: the global
 * default when the caller passed one, the built-in one otherwise.
 */
export function defaultThresholdOf(spec: SystemAlertSpec, defaults: SystemAlertRuleConfig[]): number {
  const rule = defaults.find((candidate) => candidate.metric === spec.metric)
  return rule ? rule.threshold : spec.default_threshold
}

/** defaultMinutesOf is the window of the rule a reset would restore. */
export function defaultMinutesOf(spec: SystemAlertSpec, defaults: SystemAlertRuleConfig[]): number {
  const rule = defaults.find((candidate) => candidate.metric === spec.metric)
  return rule ? rule.min_minutes : spec.default_minutes
}
